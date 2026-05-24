package config

import (
	"os"
	"strconv"
)

// Config 애플리케이션 설정
type Config struct {
	// Redis 설정
	RedisHost     string
	RedisPort     string
	RedisPassword string
	RedisDB       int

	// Backend API 설정
	BackendAPIURL string
	BackendAPIKey string

	// Terraform 설정
	TerraformBinary     string
	TerraformModulePath string
	WorkspacePath       string
	TerraformTimeout    int // Terraform 명령 타임아웃 (초)

	// Terraform State Backend (S3/MinIO)
	StateBackendEnabled bool
	StateS3Bucket       string
	StateS3Region       string
	StateS3Endpoint     string // MinIO endpoint (optional, for S3-compatible storage)
	StateS3AccessKey    string
	StateS3SecretKey    string

	// Worker 설정
	WorkerConcurrency int
	QueueName         string
}

// Load 환경변수에서 설정을 로드합니다
// 사용법: source .env.{환경} && ./provisioning-worker
func Load() *Config {

	return &Config{
		// Redis
		RedisHost:     getEnv("REDIS_HOST", "localhost"),
		RedisPort:     getEnv("REDIS_PORT", "6379"),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),
		RedisDB:       getEnvInt("REDIS_DB", 0),

		// Backend API
		BackendAPIURL: getEnv("BACKEND_API_URL", "http://localhost:8080"),
		BackendAPIKey: getEnv("BACKEND_API_KEY", "infra-worker-secret-key-change-in-production"),

		// Terraform
		TerraformBinary:     getEnv("TERRAFORM_BINARY", "terraform"),
		TerraformModulePath: getEnv("TERRAFORM_MODULE_PATH", "./terraform/modules/vsphere-vm"),
		WorkspacePath:       getEnv("WORKSPACE_PATH", "./workspace"),
		TerraformTimeout:    getEnvInt("TERRAFORM_TIMEOUT", 1800), // 기본 30분

		// Terraform State Backend
		StateBackendEnabled: getEnv("STATE_BACKEND_ENABLED", "false") == "true",
		StateS3Bucket:       getEnv("STATE_S3_BUCKET", "terraform-state"),
		StateS3Region:       getEnv("STATE_S3_REGION", "us-east-1"),
		StateS3Endpoint:     getEnv("STATE_S3_ENDPOINT", ""), // MinIO endpoint (optional)
		StateS3AccessKey:    getEnv("STATE_S3_ACCESS_KEY", ""),
		StateS3SecretKey:    getEnv("STATE_S3_SECRET_KEY", ""),

		// Worker
		WorkerConcurrency: getEnvInt("WORKER_CONCURRENCY", 10),
		QueueName:         getEnv("QUEUE_NAME", "terraform:jobs"),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return defaultValue
}
