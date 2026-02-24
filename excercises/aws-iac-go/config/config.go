package config

import (
	"log"
	"os"
)

// Config holds all infrastructure and application configuration.
type Config struct {
	AWSRegion              string
	DynamoTableName        string
	LambdaFuncName         string
	LambdaRoleName         string
	LambdaRoleARN          string // optional — if set, IAM creation is skipped entirely
	PermissionsBoundaryARN string // required in accounts that enforce IAM boundaries
	LogGroupName           string
	SNSTopicName           string
	AlertEmail             string // optional — email subscription is skipped if empty
	ExternalAPIURL         string
	MaxRetries             int
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	cfg := &Config{
		AWSRegion:              getEnv("AWS_REGION", "eu-west-1"),
		DynamoTableName:        getEnv("DYNAMO_TABLE_NAME", "iac-data-store"),
		LambdaFuncName:         getEnv("LAMBDA_FUNC_NAME", "iac-data-fetcher"),
		LambdaRoleName:         getEnv("LAMBDA_ROLE_NAME", "iac-lambda-role"),
		LambdaRoleARN:          getEnv("LAMBDA_ROLE_ARN", ""),          // e.g. arn:aws:iam::123456789:role/my-role
		PermissionsBoundaryARN: getEnv("PERMISSIONS_BOUNDARY_ARN", ""), // e.g. arn:aws:iam::123456789:policy/MyBoundary
		LogGroupName:           getEnv("LOG_GROUP_NAME", "/aws/lambda/iac-data-fetcher"),
		SNSTopicName:           getEnv("SNS_TOPIC_NAME", "iac-alerts"),
		AlertEmail:             getEnv("ALERT_EMAIL", ""),
		ExternalAPIURL:         getEnv("EXTERNAL_API_URL", "https://jsonplaceholder.typicode.com/posts"),
		MaxRetries:             3,
	}

	log.Printf("[config] Region: %s | Table: %s | Lambda: %s",
		cfg.AWSRegion, cfg.DynamoTableName, cfg.LambdaFuncName)
	return cfg
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}