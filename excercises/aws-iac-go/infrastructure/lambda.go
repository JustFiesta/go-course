package infrastructure

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"aws-iac-go/config"
	"aws-iac-go/utils"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdaTypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

// SetupLambda packages the Python handler into a ZIP and creates or updates the Lambda function.
// Returns the function ARN.
func SetupLambda(ctx context.Context, awsCfg aws.Config, cfg *config.Config, roleARN string) (string, error) {
	client := lambda.NewFromConfig(awsCfg)

	// 1. Package handler.py into a ZIP archive
	log.Printf("[lambda] Packaging handler.py into ZIP...")
	zipBytes, err := packageLambdaCode()
	if err != nil {
		return "", fmt.Errorf("packageLambdaCode: %w", err)
	}
	log.Printf("[lambda] ZIP ready, size: %d bytes", len(zipBytes))

	// 2. Environment variables injected into the Lambda function.
	// Note: AWS_REGION is reserved by Lambda and must not be set manually.
	envVars := map[string]string{
		"DYNAMODB_TABLE_NAME": cfg.DynamoTableName,
		"EXTERNAL_API_URL":    cfg.ExternalAPIURL,
		"MAX_RETRIES":         fmt.Sprintf("%d", cfg.MaxRetries),
	}

	// 3. Check whether the function already exists
	existing, err := client.GetFunction(ctx, &lambda.GetFunctionInput{
		FunctionName: aws.String(cfg.LambdaFuncName),
	})

	if err == nil {
		// Function exists — update code and configuration
		funcARN := aws.ToString(existing.Configuration.FunctionArn)
		log.Printf("[lambda] Function exists, updating: %s", funcARN)
		return updateLambda(ctx, client, cfg, zipBytes, envVars, funcARN)
	}

	// 4. Create a new function
	log.Printf("[lambda] Creating function: %s", cfg.LambdaFuncName)
	createOut, err := client.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(cfg.LambdaFuncName),
		Description:  aws.String("Fetches external API data and stores in DynamoDB"),
		Runtime:      lambdaTypes.RuntimePython312,
		Handler:      aws.String("handler.lambda_handler"),
		Role:         aws.String(roleARN),
		Timeout:      aws.Int32(60),
		MemorySize:   aws.Int32(256),
		Code: &lambdaTypes.FunctionCode{
			ZipFile: zipBytes,
		},
		Environment: &lambdaTypes.Environment{
			Variables: envVars,
		},
		Tags: map[string]string{
			"Project":   "iac-go",
			"ManagedBy": "go-sdk",
		},
	})
	if err != nil {
		return "", fmt.Errorf("CreateFunction: %w", err)
	}

	funcARN := aws.ToString(createOut.FunctionArn)
	log.Printf("[lambda] Function being created: %s", funcARN)

	// 5. Wait until the function state becomes Active
	if err = waitForLambdaActive(ctx, client, cfg.LambdaFuncName); err != nil {
		return "", err
	}

	log.Printf("[lambda] Function %s is ready", cfg.LambdaFuncName)
	return funcARN, nil
}

// updateLambda updates the code and configuration of an existing Lambda function.
func updateLambda(ctx context.Context, client *lambda.Client, cfg *config.Config,
	zipBytes []byte, envVars map[string]string, funcARN string) (string, error) {

	// Update function code
	_, err := client.UpdateFunctionCode(ctx, &lambda.UpdateFunctionCodeInput{
		FunctionName: aws.String(cfg.LambdaFuncName),
		ZipFile:      zipBytes,
	})
	if err != nil {
		return "", fmt.Errorf("UpdateFunctionCode: %w", err)
	}

	// Wait for the code update to finish before changing configuration
	if err = waitForLambdaActive(ctx, client, cfg.LambdaFuncName); err != nil {
		return "", err
	}

	// Update configuration (env vars, timeout, memory)
	_, err = client.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{
		FunctionName: aws.String(cfg.LambdaFuncName),
		Timeout:      aws.Int32(60),
		MemorySize:   aws.Int32(256),
		Environment: &lambdaTypes.Environment{
			Variables: envVars,
		},
	})
	if err != nil {
		return "", fmt.Errorf("UpdateFunctionConfiguration: %w", err)
	}

	log.Printf("[lambda] Function updated: %s", funcARN)
	return funcARN, nil
}

// waitForLambdaActive polls the function state until it reaches Active.
func waitForLambdaActive(ctx context.Context, client *lambda.Client, funcName string) error {
	log.Printf("[lambda] Waiting for Active state...")
	return utils.PollUntil(120*time.Second, 5*time.Second, func() (bool, error) {
		out, err := client.GetFunction(ctx, &lambda.GetFunctionInput{
			FunctionName: aws.String(funcName),
		})
		if err != nil {
			return false, nil
		}
		state := out.Configuration.State
		lastUpdate := out.Configuration.LastUpdateStatus
		log.Printf("[lambda] State: %s (LastUpdate: %s)", state, lastUpdate)

		// Ready when Active and last update is no longer InProgress
		return state == lambdaTypes.StateActive &&
			lastUpdate != lambdaTypes.LastUpdateStatusInProgress, nil
	})
}

// packageLambdaCode reads handler.py and packs it into a ZIP archive.
// Searches several locations to support both local runs and CI environments.
func packageLambdaCode() ([]byte, error) {
	paths := []string{
		"lambda_src/handler.py",
		"../lambda_src/handler.py",
		"/tmp/handler.py",
	}

	var handlerCode []byte
	var err error
	for _, p := range paths {
		handlerCode, err = os.ReadFile(p)
		if err == nil {
			log.Printf("[lambda] Read handler.py from: %s", p)
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("handler.py not found: %w", err)
	}

	return utils.ZipFiles(map[string][]byte{
		"handler.py": handlerCode,
	})
}

// InvokeLambdaTest performs a test invocation of the Lambda function and logs the result.
func InvokeLambdaTest(ctx context.Context, awsCfg aws.Config, cfg *config.Config) error {
	client := lambda.NewFromConfig(awsCfg)
	log.Printf("[lambda] Test invocation of %s...", cfg.LambdaFuncName)

	out, err := client.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(cfg.LambdaFuncName),
		Payload:      []byte(`{}`),
	})
	if err != nil {
		return fmt.Errorf("Invoke: %w", err)
	}

	if out.FunctionError != nil {
		log.Printf("[lambda] WARN: Function returned an error: %s | Payload: %s",
			aws.ToString(out.FunctionError), string(out.Payload))
	} else {
		log.Printf("[lambda] Test OK (StatusCode: %d) | Payload: %s",
			out.StatusCode, string(out.Payload))
	}
	return nil
}

// DeleteLambdaFunction removes the Lambda function (cleanup helper).
func DeleteLambdaFunction(ctx context.Context, awsCfg aws.Config, cfg *config.Config) error {
	client := lambda.NewFromConfig(awsCfg)
	_, err := client.DeleteFunction(ctx, &lambda.DeleteFunctionInput{
		FunctionName: aws.String(cfg.LambdaFuncName),
	})
	if err != nil && !strings.Contains(err.Error(), "ResourceNotFoundException") {
		return fmt.Errorf("DeleteFunction: %w", err)
	}
	log.Printf("[lambda] Function %s deleted", cfg.LambdaFuncName)
	return nil
}