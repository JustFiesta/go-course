package infrastructure

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"aws-iac-go/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwTypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
)

// SetupCloudWatch creates the log group, metric alarms, and dashboard for the Lambda function.
func SetupCloudWatch(ctx context.Context, awsCfg aws.Config, cfg *config.Config, funcARN, snsARN string) error {
	if err := setupLogGroup(ctx, awsCfg, cfg); err != nil {
		return err
	}
	if err := setupAlarms(ctx, awsCfg, cfg, snsARN); err != nil {
		return err
	}
	if err := setupDashboard(ctx, awsCfg, cfg); err != nil {
		// Dashboard is optional — do not abort the deployment
		log.Printf("[cloudwatch] WARN: Could not create dashboard: %v", err)
	}
	return nil
}

// ── Log Group ────────────────────────────────────────────────────────────────

func setupLogGroup(ctx context.Context, awsCfg aws.Config, cfg *config.Config) error {
	client := cloudwatchlogs.NewFromConfig(awsCfg)

	// CreateLogGroup is idempotent — safe to call if the group already exists
	log.Printf("[cloudwatch] Creating log group: %s", cfg.LogGroupName)
	_, err := client.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(cfg.LogGroupName),
		Tags: map[string]string{
			"Project":   "iac-go",
			"ManagedBy": "go-sdk",
		},
	})
	if err != nil && !strings.Contains(err.Error(), "ResourceAlreadyExistsException") {
		return fmt.Errorf("CreateLogGroup: %w", err)
	}

	// Set 30-day retention to control storage costs
	_, err = client.PutRetentionPolicy(ctx, &cloudwatchlogs.PutRetentionPolicyInput{
		LogGroupName:    aws.String(cfg.LogGroupName),
		RetentionInDays: aws.Int32(30),
	})
	if err != nil {
		log.Printf("[cloudwatch] WARN: Could not set log retention: %v", err)
	}

	log.Printf("[cloudwatch] Log group ready: %s (retention: 30 days)", cfg.LogGroupName)
	return nil
}

// ── CloudWatch Alarms ────────────────────────────────────────────────────────

func setupAlarms(ctx context.Context, awsCfg aws.Config, cfg *config.Config, snsARN string) error {
	client := cloudwatch.NewFromConfig(awsCfg)

	alarms := []struct {
		name       string
		metric     string
		threshold  float64
		period     int32
		comparison cwTypes.ComparisonOperator
		stat       string
		desc       string
	}{
		{
			name:       fmt.Sprintf("%s-errors", cfg.LambdaFuncName),
			metric:     "Errors",
			threshold:  1,
			period:     300, // 5 minutes
			comparison: cwTypes.ComparisonOperatorGreaterThanOrEqualToThreshold,
			stat:       "Sum",
			desc:       "Alert when the Lambda function returns an error",
		},
		{
			name:       fmt.Sprintf("%s-duration", cfg.LambdaFuncName),
			metric:     "Duration",
			threshold:  45000, // 45s = 75% of the 60s timeout
			period:     300,
			comparison: cwTypes.ComparisonOperatorGreaterThanThreshold,
			stat:       "Average",
			desc:       "Alert when Lambda execution time is too high",
		},
		{
			name:       fmt.Sprintf("%s-throttles", cfg.LambdaFuncName),
			metric:     "Throttles",
			threshold:  1,
			period:     300,
			comparison: cwTypes.ComparisonOperatorGreaterThanOrEqualToThreshold,
			stat:       "Sum",
			desc:       "Alert when Lambda is being throttled",
		},
	}

	alarmActions := []string{}
	if snsARN != "" {
		alarmActions = []string{snsARN}
	}

	for _, a := range alarms {
		log.Printf("[cloudwatch] Creating alarm: %s", a.name)
		input := &cloudwatch.PutMetricAlarmInput{
			AlarmName:          aws.String(a.name),
			AlarmDescription:   aws.String(a.desc),
			MetricName:         aws.String(a.metric),
			Namespace:          aws.String("AWS/Lambda"),
			Statistic:          cwTypes.Statistic(a.stat),
			Period:             aws.Int32(a.period),
			EvaluationPeriods:  aws.Int32(1),
			Threshold:          aws.Float64(a.threshold),
			ComparisonOperator: a.comparison,
			TreatMissingData:   aws.String("notBreaching"),
			Dimensions: []cwTypes.Dimension{
				{
					Name:  aws.String("FunctionName"),
					Value: aws.String(cfg.LambdaFuncName),
				},
			},
		}

		if len(alarmActions) > 0 {
			input.AlarmActions = alarmActions
			input.OKActions = alarmActions
		}

		_, err := client.PutMetricAlarm(ctx, input)
		if err != nil {
			return fmt.Errorf("PutMetricAlarm %s: %w", a.name, err)
		}
	}

	log.Printf("[cloudwatch] Created %d alarms", len(alarms))
	return nil
}

// ── Dashboard ────────────────────────────────────────────────────────────────

func setupDashboard(ctx context.Context, awsCfg aws.Config, cfg *config.Config) error {
	client := cloudwatch.NewFromConfig(awsCfg)
	dashboardName := fmt.Sprintf("%s-dashboard", cfg.LambdaFuncName)

	// Four widgets: Lambda invocations/errors, duration, DynamoDB ops, custom metrics
	widgets := []map[string]any{
		{
			"type":   "metric",
			"width":  12,
			"height": 6,
			"properties": map[string]any{
				"title":  "Lambda Invocations & Errors",
				"period": 300,
				"stat":   "Sum",
				"metrics": [][]any{
					{"AWS/Lambda", "Invocations", "FunctionName", cfg.LambdaFuncName},
					{"AWS/Lambda", "Errors",      "FunctionName", cfg.LambdaFuncName},
					{"AWS/Lambda", "Throttles",   "FunctionName", cfg.LambdaFuncName},
				},
			},
		},
		{
			"type":   "metric",
			"width":  12,
			"height": 6,
			"properties": map[string]any{
				"title":  "Lambda Duration (ms)",
				"period": 300,
				"stat":   "Average",
				"metrics": [][]any{
					{"AWS/Lambda", "Duration", "FunctionName", cfg.LambdaFuncName},
				},
			},
		},
		{
			"type":   "metric",
			"width":  12,
			"height": 6,
			"properties": map[string]any{
				"title":  "DynamoDB Operations",
				"period": 300,
				"stat":   "Sum",
				"metrics": [][]any{
					{"AWS/DynamoDB", "SuccessfulRequestLatency", "TableName", cfg.DynamoTableName, "Operation", "PutItem"},
					{"AWS/DynamoDB", "SystemErrors",             "TableName", cfg.DynamoTableName},
				},
			},
		},
		{
			"type":   "metric",
			"width":  12,
			"height": 6,
			"properties": map[string]any{
				"title":  "Custom: Processed Records",
				"period": 300,
				"stat":   "Sum",
				"metrics": [][]any{
					{"IaC/Lambda", "ProcessedRecords", "FunctionName", cfg.LambdaFuncName},
					{"IaC/Lambda", "Errors",           "FunctionName", cfg.LambdaFuncName},
				},
			},
		},
	}

	dashBody := map[string]any{"widgets": widgets}
	dashJSON, err := json.Marshal(dashBody)
	if err != nil {
		return fmt.Errorf("marshal dashboard: %w", err)
	}

	_, err = client.PutDashboard(ctx, &cloudwatch.PutDashboardInput{
		DashboardName: aws.String(dashboardName),
		DashboardBody: aws.String(string(dashJSON)),
	})
	if err != nil {
		return fmt.Errorf("PutDashboard: %w", err)
	}

	log.Printf("[cloudwatch] Dashboard created: %s", dashboardName)
	return nil
}

// DeleteCloudWatchResources removes alarms, dashboard and log group (cleanup helper).
func DeleteCloudWatchResources(ctx context.Context, awsCfg aws.Config, cfg *config.Config) {
	cwClient := cloudwatch.NewFromConfig(awsCfg)
	logsClient := cloudwatchlogs.NewFromConfig(awsCfg)

	alarmNames := []string{
		fmt.Sprintf("%s-errors", cfg.LambdaFuncName),
		fmt.Sprintf("%s-duration", cfg.LambdaFuncName),
		fmt.Sprintf("%s-throttles", cfg.LambdaFuncName),
	}
	cwClient.DeleteAlarms(ctx, &cloudwatch.DeleteAlarmsInput{AlarmNames: alarmNames}) //nolint

	cwClient.DeleteDashboards(ctx, &cloudwatch.DeleteDashboardsInput{ //nolint
		DashboardNames: []string{fmt.Sprintf("%s-dashboard", cfg.LambdaFuncName)},
	})

	logsClient.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{ //nolint
		LogGroupName: aws.String(cfg.LogGroupName),
	})

	log.Printf("[cloudwatch] CloudWatch resources deleted")
}