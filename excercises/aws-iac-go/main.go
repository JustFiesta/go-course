package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"aws-iac-go/config"
	"aws-iac-go/infrastructure"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

func main() {
	// ── CLI flags ──────────────────────────────────────────────────────────
	destroy := flag.Bool("destroy", false, "Tear down all provisioned infrastructure")
	testRun := flag.Bool("test", false,   "Invoke the Lambda function after deployment")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("│ ")

	printBanner()

	// ── Load configuration ─────────────────────────────────────────────────
	cfg := config.Load()
	ctx := context.Background()

	// ── AWS SDK config (reads env vars / ~/.aws/credentials automatically) ──
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.AWSRegion),
	)
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}
	log.Printf("[main] AWS region: %s", cfg.AWSRegion)

	// ── Destroy mode ───────────────────────────────────────────────────────
	if *destroy {
		runDestroy(ctx, awsCfg, cfg)
		return
	}

	// ── Deploy mode ────────────────────────────────────────────────────────
	runDeploy(ctx, awsCfg, cfg, *testRun)
}

// runDeploy provisions all infrastructure in dependency order.
func runDeploy(ctx context.Context, awsCfg aws.Config, cfg *config.Config, runTest bool) {
	start := time.Now()
	log.Println("[main] ══════════════════════════════════════")
	log.Println("[main]   DEPLOYMENT STARTED                 ")
	log.Println("[main] ══════════════════════════════════════")

	// Step 1: SNS — needed early so alarms have an action target
	log.Println("[main] [1/5] Setting up SNS...")
	snsARN, err := infrastructure.SetupSNS(ctx, awsCfg, cfg)
	mustSucceed(err, "SNS")

	// Step 2: IAM — must exist before Lambda can be created
	log.Println("[main] [2/5] Setting up IAM...")
	roleARN, err := infrastructure.SetupIAM(ctx, awsCfg, cfg)
	mustSucceed(err, "IAM")

	// Step 3: DynamoDB — independent of Lambda, could run in parallel in future
	log.Println("[main] [3/5] Setting up DynamoDB...")
	_, err = infrastructure.SetupDynamoDB(ctx, awsCfg, cfg)
	mustSucceed(err, "DynamoDB")

	// Step 4: Lambda — requires IAM role ARN and table name
	log.Println("[main] [4/5] Deploying Lambda function...")
	funcARN, err := infrastructure.SetupLambda(ctx, awsCfg, cfg, roleARN)
	mustSucceed(err, "Lambda")

	// Step 5: CloudWatch — requires function ARN and SNS topic ARN
	log.Println("[main] [5/5] Setting up CloudWatch...")
	err = infrastructure.SetupCloudWatch(ctx, awsCfg, cfg, funcARN, snsARN)
	mustSucceed(err, "CloudWatch")

	// ── Health checks ──────────────────────────────────────────────────────
	log.Println("[main] ── Health checks ──")
	if err := infrastructure.DynamoDBHealthCheck(ctx, awsCfg, cfg); err != nil {
		log.Printf("[main] WARN: DynamoDB health check failed: %v", err)
	}

	// ── Optional test invocation ───────────────────────────────────────────
	if runTest {
		log.Println("[main] ── Test invocation ──")
		if err := infrastructure.InvokeLambdaTest(ctx, awsCfg, cfg); err != nil {
			log.Printf("[main] WARN: Lambda test failed: %v", err)
		}
	}

	// ── Summary ────────────────────────────────────────────────────────────
	elapsed := time.Since(start).Round(time.Second)
	log.Println("[main] ══════════════════════════════════════")
	log.Printf("[main]   ✅ DEPLOYMENT COMPLETE (%s)         ", elapsed)
	log.Println("[main] ══════════════════════════════════════")
	printSummary(cfg, funcARN, snsARN)
}

// runDestroy tears down all provisioned resources in reverse dependency order.
func runDestroy(ctx context.Context, awsCfg aws.Config, cfg *config.Config) {
	log.Println("[main] ══════════════════════════════════════")
	log.Println("[main]   DESTROY: removing infrastructure    ")
	log.Println("[main] ══════════════════════════════════════")

	infrastructure.DeleteCloudWatchResources(ctx, awsCfg, cfg)
	infrastructure.DeleteLambdaFunction(ctx, awsCfg, cfg)

	// SNS ARN is reconstructed from the topic name (simplification).
	// In production, store ARNs in a state file or SSM Parameter Store.
	log.Println("[main] Removing SNS, IAM and DynamoDB resources...")
	infrastructure.DeleteIAMRole(ctx, awsCfg, cfg)     //nolint
	infrastructure.DeleteDynamoTable(ctx, awsCfg, cfg) //nolint

	log.Println("[main] ✅ Infrastructure removed")
}

// mustSucceed terminates the program with a fatal error if err is non-nil.
func mustSucceed(err error, step string) {
	if err != nil {
		log.Fatalf("[main] FATAL at step '%s': %v", step, err)
		os.Exit(1)
	}
}

func printBanner() {
	log.Println("╔══════════════════════════════════════════╗")
	log.Println("║  AWS IaC — Go SDK                        ║")
	log.Println("║  DynamoDB + Lambda + CloudWatch          ║")
	log.Println("╚══════════════════════════════════════════╝")
}

func printSummary(cfg *config.Config, funcARN, snsARN string) {
	log.Printf("[summary] %-20s %s", "Lambda ARN:", funcARN)
	log.Printf("[summary] %-20s %s", "DynamoDB Table:", cfg.DynamoTableName)
	log.Printf("[summary] %-20s %s", "SNS Topic ARN:", snsARN)
	log.Printf("[summary] %-20s %s", "Log Group:", cfg.LogGroupName)
	log.Printf("[summary] %-20s %s", "Region:", cfg.AWSRegion)
	log.Printf("[summary]")
	log.Printf("[summary] Run with -test to invoke the Lambda after deployment:")
	log.Printf("[summary]   go run . -test")
	log.Printf("[summary]")
	log.Printf("[summary] To remove all infrastructure:")
	log.Printf("[summary]   go run . -destroy")
}