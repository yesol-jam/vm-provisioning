package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/yesol-jam/vm-provisioning/internal/config"
	"github.com/yesol-jam/vm-provisioning/internal/worker"
	"github.com/sirupsen/logrus"
)

func main() {
	// 로거 설정
	logger := logrus.New()
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})
	logger.SetLevel(logrus.InfoLevel)

	if os.Getenv("DEBUG") == "true" {
		logger.SetLevel(logrus.DebugLevel)
	}

	logger.Info("===== vm-provisioning-worker 시작 =====")

	// 설정 로드
	cfg := config.Load()
	logger.Infof("설정 로드 완료: backendAPI=%s, redis=%s:%s, concurrency=%d",
		cfg.BackendAPIURL, cfg.RedisHost, cfg.RedisPort, cfg.WorkerConcurrency)

	// 워커 생성
	w, err := worker.New(cfg, logger)
	if err != nil {
		logger.Fatalf("워커 생성 실패: %v", err)
	}

	// 워커 시작
	w.Start()

	// 시그널 대기 (SIGINT, SIGTERM)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	logger.Infof("시그널 수신: %v", sig)

	// 워커 종료
	w.Stop()

	logger.Info("===== vm-provisioning-worker 종료 =====")
}
