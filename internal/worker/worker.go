package worker

import (
	"context"
	"sync"
	"time"

	"github.com/itraining/provisioning-worker/internal/api"
	"github.com/itraining/provisioning-worker/internal/config"
	"github.com/itraining/provisioning-worker/internal/queue"
	"github.com/itraining/provisioning-worker/internal/terraform"
	"github.com/sirupsen/logrus"
)

// Worker Terraform 작업 워커
type Worker struct {
	config        *config.Config
	queue         *queue.RedisQueue
	reliableQueue *queue.ReliableRedisQueue
	executor      *terraform.Executor
	apiClient     *api.Client
	logger        *logrus.Logger

	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
}

// New 새 워커 생성
func New(cfg *config.Config, logger *logrus.Logger) (*Worker, error) {
	// Redis 큐 초기화
	redisQueue, err := queue.NewRedisQueue(cfg, logger)
	if err != nil {
		return nil, err
	}

	// Reliable Queue 초기화
	reliableQueue := queue.NewReliableRedisQueue(redisQueue.GetClient(), logger)

	// API 클라이언트 초기화
	apiClient := api.NewClient(cfg.BackendAPIURL, cfg.BackendAPIKey, logger)

	// Terraform 실행기 초기화
	executor := terraform.NewExecutor(cfg, apiClient, logger)

	ctx, cancel := context.WithCancel(context.Background())

	return &Worker{
		config:        cfg,
		queue:         redisQueue,
		reliableQueue: reliableQueue,
		executor:      executor,
		apiClient:     apiClient,
		logger:        logger,
		ctx:           ctx,
		cancel:        cancel,
	}, nil
}

// Start 워커 시작
func (w *Worker) Start() {
	w.logger.Infof("워커 시작: concurrency=%d, reliable_queue=enabled", w.config.WorkerConcurrency)

	// 동시 실행 워커 시작
	for i := 0; i < w.config.WorkerConcurrency; i++ {
		w.wg.Add(1)
		go w.workerLoop(i)
	}

	// Stalled Job Recovery 고루틴 시작 (5분마다)
	w.wg.Add(1)
	go w.stalledJobRecoveryLoop()
}

// workerLoop 워커 루프 (Reliable Queue 사용)
func (w *Worker) workerLoop(workerID int) {
	defer w.wg.Done()

	w.logger.Infof("워커 #%d 시작 (Reliable Queue 모드)", workerID)

	for {
		select {
		case <-w.ctx.Done():
			w.logger.Infof("워커 #%d 종료", workerID)
			return
		default:
			// Reliable Queue로 작업 가져오기 (BRPOPLPUSH)
			// JobWithRawJSON에 원본 JSON이 포함되어 AckJob에서 정확한 매칭 가능
			jobWrapper, err := w.reliableQueue.PopReliable(w.ctx)
			if err != nil {
				if w.ctx.Err() != nil {
					return // 컨텍스트 취소됨
				}
				w.logger.Errorf("워커 #%d: 작업 가져오기 실패: %v", workerID, err)
				continue
			}

			if jobWrapper == nil {
				continue // 타임아웃, 다시 시도
			}

			// 작업 실행 (Job 구조체 전달)
			job := jobWrapper.Job
			w.logger.Infof("워커 #%d: 작업 실행 시작: executionID=%d", workerID, job.ExecutionID)

			err = w.executor.Execute(job)

			if err != nil {
				w.logger.Errorf("워커 #%d: 작업 실행 실패: %v", workerID, err)
				// 작업 실패 - processing 큐에서 제거 (재시도하지 않음)
				// Terraform 실행 실패는 이미 Backend에 FAILED로 기록됨
				w.reliableQueue.AckJob(w.ctx, jobWrapper)
			} else {
				w.logger.Infof("워커 #%d: 작업 실행 완료: executionID=%d", workerID, job.ExecutionID)
				// 작업 성공 - processing 큐에서 제거 (원본 JSON으로 정확한 매칭)
				w.reliableQueue.AckJob(w.ctx, jobWrapper)
			}
		}
	}
}

// stalledJobRecoveryLoop 멈춘 작업 복구 루프 (5분마다)
func (w *Worker) stalledJobRecoveryLoop() {
	defer w.wg.Done()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	w.logger.Info("Stalled Job Recovery 루프 시작 (5분 간격)")

	for {
		select {
		case <-w.ctx.Done():
			w.logger.Info("Stalled Job Recovery 루프 종료")
			return
		case <-ticker.C:
			processingCount, _ := w.reliableQueue.GetProcessingCount(w.ctx)
			if processingCount > 0 {
				w.logger.Infof("Processing 큐 모니터링: count=%d", processingCount)

				// Stalled Job 복구 실행
				recovered, err := w.reliableQueue.RecoverStalledJobs(w.ctx, w.apiClient)
				if err != nil {
					w.logger.Errorf("Stalled Job Recovery 실패: %v", err)
				} else if recovered > 0 {
					w.logger.Infof("Stalled Job Recovery 성공: %d개 작업 복구", recovered)
				}
			}
		}
	}
}

// Stop 워커 종료
func (w *Worker) Stop() {
	w.logger.Info("워커 종료 중...")
	w.cancel()
	w.wg.Wait()
	w.queue.Close()
	w.logger.Info("워커 종료 완료")
}

// Wait 워커 종료 대기
func (w *Worker) Wait() {
	w.wg.Wait()
}
