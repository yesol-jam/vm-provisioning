# VM Provisioning Worker

Redis 큐에서 작업을 수신하여 Terraform으로 vSphere VM을 자동 배포하는 Go 기반 워커 시스템입니다.

> **개발 기간**: 2025.01 – 2025.05  
> **역할**: 워커 시스템 단독 설계 및 구현, Terraform 모듈 작성  
> **기술 스택**: Go, Redis, Terraform, vSphere, Docker

---

## 프로젝트 배경

이러닝 플랫폼에서 수강생별로 독립된 실습 VM을 제공하는 기능을 구축해야 했습니다. 핵심 과제는 두 가지였습니다.

1. **대규모 VM 동시 배포**: 강의 시작 시 수십 개의 VM이 동시에 요청되므로, 백엔드 API가 직접 Terraform을 실행하면 타임아웃과 병목이 발생합니다.
2. **작업 유실 방지**: VM 배포 도중 워커가 재시작되거나 네트워크가 끊어져도 진행 중이던 작업이 사라지면 안 됩니다.

이를 해결하기 위해 백엔드와 완전히 분리된 **비동기 워커 패턴**을 설계했습니다. 백엔드는 Redis 큐에 작업을 넣고 즉시 응답을 반환하며, 워커가 큐에서 작업을 꺼내 Terraform을 실행하고 완료 후 결과를 API로 전송합니다.

---

## 시스템 개요

```
Backend (Spring Boot)
    │
    │  TerraformJobPayload (JSON)
    ▼
Redis Queue (terraform:jobs)
    │
    │  BRPOPLPUSH (Reliable Queue)
    ▼
Provisioning Worker (this)
    │
    ├── 1. Backend API에서 프로젝트 상세 조회
    │        (vCenter 자격증명, 네트워크, VM 사양)
    │
    ├── 2. terraform.tfvars.json 자동 생성
    │
    ├── 3. terraform init / plan / apply / destroy 실행
    │
    ├── 4. Apply 성공 시: VM 정보(MOID, UUID, IP) Backend에 등록
    │
    └── 5. 실패 시: 상태 FAILED 업데이트 + terraform destroy 자동 롤백
```

---

## 핵심 구현

### Reliable Queue (작업 유실 방지)

Redis의 단순 Pop은 워커가 작업을 꺼낸 직후 크래시하면 작업이 영구 소실됩니다. `BRPOPLPUSH`를 사용해 작업을 `jobs` 큐에서 `processing` 큐로 **원자적으로 이동**시키고, 완료 확인(ACK) 후에만 제거합니다.

```go
// jobs 큐에서 꺼내는 동시에 processing 큐에 원자적으로 삽입
func (q *ReliableRedisQueue) PopReliable(ctx context.Context) (*JobWithRawJSON, error) {
    result, err := q.client.BRPopLPush(ctx,
        q.jobQueue,        // terraform:jobs
        q.processingQueue, // terraform:processing
        timeout,
    ).Result()
    // 원본 JSON 보존 + 타임스탬프 기록
}

// 작업 완료 후 processing 큐에서 제거
func (q *ReliableRedisQueue) AckJob(ctx context.Context, job *JobWithRawJSON) {
    q.client.LRem(ctx, q.processingQueue, 1, job.RawJSON)
}
```

### Stalled Job Recovery (장애 자동 복구)

워커가 비정상 종료되면 `processing` 큐에 작업이 남습니다. 5분마다 스케줄러가 큐를 순회하여 **30분 이상 경과한 작업**을 자동으로 복구합니다.

```go
// 5분마다 실행
ticker := time.NewTicker(5 * time.Minute)

// Backend API로 실제 상태 확인
status, _ := apiClient.GetExecutionStatus(job.ExecutionID)
switch status {
case "SUCCESS", "FAILED":
    q.AckJob(ctx, job)       // 이미 완료 → 제거
case "QUEUED", "RUNNING":
    q.NackJob(ctx, job)      // 재처리 필요 → jobs 큐로 복귀
case 404:
    q.AckJob(ctx, job)       // DB 미존재(고아 작업) → 제거
}
```

### Exponential Backoff 재시도

Terraform 실행 중 Backend API 일시 장애가 발생해도 작업이 실패하지 않도록, 서버 에러(5xx)에 한해 최대 3회 재시도합니다. 클라이언트 에러(4xx)는 재시도하지 않습니다.

```go
// 1초 → 2초 → 4초 (최대 30초, jitter ±20% 포함)
delay := min(baseDelay * (1 << attempt), maxDelay)
delay += jitter(delay, 0.2)

// 4xx는 재시도 안함
if resp.StatusCode >= 400 && resp.StatusCode < 500 {
    return err
}
```

### 비밀번호 보안 처리

vSphere 비밀번호를 Terraform 명령행 인자로 넘기면 `ps aux`에 노출됩니다. 환경변수로만 전달하여 프로세스 목록에 비밀번호가 나타나지 않게 했습니다.

```go
cmd := exec.CommandContext(ctx, terraformBinary, args...)
cmd.Env = append(os.Environ(),
    fmt.Sprintf("TF_VAR_vsphere_password=%s", password), // 환경변수로만 전달
)
// terraform apply -auto-approve -no-color  ← 비밀번호 없음
```

### 자동 롤백

VM 배포 후 Backend에 VM 정보 등록이 실패하면, 이미 생성된 VM이 미등록 상태로 남습니다. 이를 방지하기 위해 등록 실패 시 `terraform destroy`를 자동 실행합니다.

```go
if err := e.registerInstanceVms(ctx, job, workDir); err != nil {
    // VM 등록 실패 → 상태 FAILED + terraform destroy 실행
    e.handleError(job, err)
    e.runTerraformWithOutput(ctx, workDir, executionID, password, "destroy", "-auto-approve")
}
```

---

## Terraform 모듈 (vSphere VM)

vSphere 환경에서 VM 배포에 필요한 모든 리소스를 자동으로 생성하는 Terraform 모듈을 직접 작성했습니다.

**리소스 생성 순서**

```
1. 데이터 소스 조회 (병렬)
   └─ 데이터센터, 클러스터, 데이터스토어, Content Library 조회

2. VM 폴더 생성 (Level별 순차)
   └─ iTraining → iTraining/project-1 → iTraining/project-1/vm-name

3. 네트워크 포트그룹 생성 (병렬 후 15초 안정화 대기)
   └─ DVS(클러스터) 또는 vSwitch(단일 호스트) 분기 처리

4. VM 생성 (병렬, 폴더+네트워크 완료 후)
   └─ Content Library OVF 기반 Linked Clone

5. VM 전원 차단
   └─ vSphere REST API 호출 (local-exec provisioner)
```

**주요 설계 포인트**
- 클러스터 환경(DVS)과 단일 ESXi 호스트(vSwitch) 모두 동일 모듈로 처리
- `existing_networks` 변수로 기존 네트워크 재사용 vs 신규 생성 분기
- Linked Clone으로 전체 복사 대비 배포 시간 대폭 단축

---

## 프로젝트 구조

```
vm-provisioning/
├── cmd/worker/
│   └── main.go                          # 엔트리포인트, 시그널 처리, 워커 시작
├── internal/
│   ├── api/
│   │   ├── client.go                    # Backend Internal API 클라이언트
│   │   └── retry_client.go             # Exponential Backoff 재시도 래퍼
│   ├── config/
│   │   └── config.go                   # 환경변수 로드, Config 구조체
│   ├── queue/
│   │   ├── redis.go                    # Redis 연결
│   │   └── reliable_queue.go           # BRPOPLPUSH, AckJob, NackJob, RecoverStalledJobs
│   ├── terraform/
│   │   ├── executor.go                 # Terraform 실행, tfvars 생성, VM 등록, 롤백
│   │   └── executor_test.go            # 단위 테스트
│   └── worker/
│       └── worker.go                   # 워커 루프, 동시성 관리, Recovery 스케줄러
├── pkg/models/
│   └── models.go                       # 공용 데이터 모델
├── terraform/modules/vsphere-vm/
│   ├── main.tf                         # vSphere 리소스 정의 (VM, 폴더)
│   ├── networks.tf                     # 네트워크 포트그룹 생성
│   ├── locals.tf                       # 로컬 변수 (네트워크/폴더 맵핑)
│   ├── variables.tf                    # 입력 변수
│   ├── outputs.tf                      # VM 정보 출력 (MOID, UUID, IP)
│   └── wait_for_vm_deletion.sh         # VM 삭제 완료 대기 스크립트
├── go.mod
└── go.sum
```

---

## 주요 데이터 구조

**큐 메시지 (Redis)**
```json
{
  "jobType": "TENANT",
  "tenantId": "tenant-123",
  "projectId": 1,
  "executionId": 100,
  "type": "APPLY",
  "backendApiUrl": "http://api:8080"
}
```

**Terraform 출력 → VM 등록 요청**
```json
{
  "provisioningVmId": 1,
  "vmName": "web-server-01",
  "vmMoRef": "vm-123",
  "vmUuid": "550e8400-e29b-41d4-a716-446655440000",
  "cpuCount": 4,
  "memoryMb": 8192,
  "ipAddress": "192.168.1.100"
}
```

**실행 상태**

| 상태 | 설명 |
|------|------|
| `QUEUED` | Redis 큐 대기 중 |
| `RUNNING` | Terraform 실행 중 |
| `SUCCESS` | 배포 완료, VM 등록 완료 |
| `FAILED` | 실패, 롤백 완료 |

---

## 기술적 의사결정

**왜 BRPOPLPUSH인가?**  
단순 BRPOP은 작업을 꺼낸 직후 워커가 죽으면 작업이 소실됩니다. BRPOPLPUSH는 꺼내는 동시에 다른 큐에 원자적으로 삽입하므로, 워커 크래시 후 재시작하면 `processing` 큐에서 작업을 다시 발견할 수 있습니다.

**왜 Go인가?**  
Terraform 실행 자체는 외부 프로세스이고 워커의 역할은 큐 폴링·API 호출·프로세스 관리입니다. Go의 Goroutine은 동시 10개 워커를 OS 스레드 없이 가볍게 처리하며, `exec.CommandContext`로 타임아웃 제어도 명확합니다.

**왜 Terraform인가?**  
vSphere SDK를 직접 사용하면 VM 생성·네트워크·폴더 관리 코드를 모두 직접 작성해야 합니다. Terraform vSphere Provider는 리소스 간 의존성 관리와 상태(state) 추적을 내장하고 있어, `apply`와 `destroy`가 대칭적으로 동작합니다.

---

## Tech Stack

| 분류 | 기술 |
|------|------|
| Language | Go 1.21 |
| Message Queue | Redis (BRPOPLPUSH Reliable Queue) |
| IaC | Terraform 1.0+ / vSphere Provider ~> 2.0 |
| Logging | logrus |
| Virtualization | VMware vSphere (vCenter / ESXi) |
| Containerization | Docker (Multi-stage build) |
