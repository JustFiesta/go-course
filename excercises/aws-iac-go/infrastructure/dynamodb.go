package infrastructure

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"aws-iac-go/config"
	"aws-iac-go/utils"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// SetupDynamoDB creates a DynamoDB table and waits until it becomes active.
// If the table already exists, it returns its ARN without making any changes.
func SetupDynamoDB(ctx context.Context, awsCfg aws.Config, cfg *config.Config) (string, error) {
	client := dynamodb.NewFromConfig(awsCfg)

	// 1. Check whether the table already exists
	tableARN, err := getTableARN(ctx, client, cfg.DynamoTableName)
	if err == nil {
		log.Printf("[dynamodb] Table %s already exists: %s", cfg.DynamoTableName, tableARN)
		return tableARN, nil
	}

	// 2. Create the table
	log.Printf("[dynamodb] Creating table: %s", cfg.DynamoTableName)
	createOut, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(cfg.DynamoTableName),

		// Schema: partition key = "id" (String), sort key = "timestamp" (String)
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("id"),        AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("timestamp"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("id"),        KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("timestamp"), KeyType: types.KeyTypeRange},
		},

		// PAY_PER_REQUEST — no cost when idle, ideal for this use case
		BillingMode: types.BillingModePayPerRequest,

		Tags: []types.Tag{
			{Key: aws.String("Project"),   Value: aws.String("iac-go")},
			{Key: aws.String("ManagedBy"), Value: aws.String("go-sdk")},
		},
	})
	if err != nil {
		if strings.Contains(err.Error(), "ResourceInUseException") {
			log.Printf("[dynamodb] Table already exists (race condition), fetching ARN")
			return getTableARN(ctx, client, cfg.DynamoTableName)
		}
		return "", fmt.Errorf("CreateTable: %w", err)
	}

	tableARN = aws.ToString(createOut.TableDescription.TableArn)
	log.Printf("[dynamodb] Table being created, ARN: %s", tableARN)

	// 3. Wait until the table reaches ACTIVE status
	log.Printf("[dynamodb] Waiting for ACTIVE status...")
	err = utils.PollUntil(120*time.Second, 5*time.Second, func() (bool, error) {
		arn, e := getTableARN(ctx, client, cfg.DynamoTableName)
		if e != nil {
			return false, nil // not ready yet
		}
		tableARN = arn

		desc, e := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
			TableName: aws.String(cfg.DynamoTableName),
		})
		if e != nil {
			return false, nil
		}
		status := desc.Table.TableStatus
		log.Printf("[dynamodb] Status: %s", status)
		return status == types.TableStatusActive, nil
	})
	if err != nil {
		return "", fmt.Errorf("timed out waiting for table: %w", err)
	}

	// 4. Enable Point-In-Time Recovery
	log.Printf("[dynamodb] Enabling Point-In-Time Recovery...")
	_, err = client.UpdateContinuousBackups(ctx, &dynamodb.UpdateContinuousBackupsInput{
		TableName: aws.String(cfg.DynamoTableName),
		PointInTimeRecoverySpecification: &types.PointInTimeRecoverySpecification{
			PointInTimeRecoveryEnabled: aws.Bool(true),
		},
	})
	if err != nil {
		// Non-fatal — PITR is optional
		log.Printf("[dynamodb] WARN: Could not enable PITR: %v", err)
	}

	log.Printf("[dynamodb] Table %s is ready", cfg.DynamoTableName)
	return tableARN, nil
}

// DynamoDBHealthCheck verifies that the table is active and accessible.
func DynamoDBHealthCheck(ctx context.Context, awsCfg aws.Config, cfg *config.Config) error {
	client := dynamodb.NewFromConfig(awsCfg)
	desc, err := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(cfg.DynamoTableName),
	})
	if err != nil {
		return fmt.Errorf("DynamoDB health check: %w", err)
	}
	if desc.Table.TableStatus != types.TableStatusActive {
		return fmt.Errorf("table %s is not ACTIVE (status: %s)",
			cfg.DynamoTableName, desc.Table.TableStatus)
	}
	log.Printf("[dynamodb] Health check OK — table ACTIVE, item count: %d",
		desc.Table.ItemCount)
	return nil
}

// getTableARN returns the ARN of an existing table.
func getTableARN(ctx context.Context, client *dynamodb.Client, tableName string) (string, error) {
	out, err := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(tableName),
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.Table.TableArn), nil
}

// DeleteDynamoTable removes the DynamoDB table (cleanup helper).
func DeleteDynamoTable(ctx context.Context, awsCfg aws.Config, cfg *config.Config) error {
	client := dynamodb.NewFromConfig(awsCfg)
	_, err := client.DeleteTable(ctx, &dynamodb.DeleteTableInput{
		TableName: aws.String(cfg.DynamoTableName),
	})
	if err != nil && !strings.Contains(err.Error(), "ResourceNotFoundException") {
		return fmt.Errorf("DeleteTable: %w", err)
	}
	log.Printf("[dynamodb] Table %s deleted", cfg.DynamoTableName)
	return nil
}