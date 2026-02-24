package infrastructure

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"aws-iac-go/config"
	"aws-iac-go/utils"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"
)

// SetupIAM creates an IAM role for Lambda with all required permissions.
// If LAMBDA_ROLE_ARN env var is set, skips creation and uses that ARN directly
// (useful when the caller lacks iam:CreateRole permissions).
// Returns the role ARN ready to be used when creating the Lambda function.
func SetupIAM(ctx context.Context, awsCfg aws.Config, cfg *config.Config) (string, error) {
	// Allow bypassing IAM creation by supplying a pre-existing role ARN
	if cfg.LambdaRoleARN != "" {
		log.Printf("[iam] Using pre-existing role ARN from LAMBDA_ROLE_ARN: %s", cfg.LambdaRoleARN)
		return cfg.LambdaRoleARN, nil
	}

	client := iam.NewFromConfig(awsCfg)

	// 1. Check whether the role already exists
	roleARN, err := getRoleARN(ctx, client, cfg.LambdaRoleName)
	if err == nil {
		log.Printf("[iam] Role %s already exists: %s", cfg.LambdaRoleName, roleARN)
		return roleARN, nil
	}

	// 2. Create the role with a trust policy allowing Lambda to assume it
	log.Printf("[iam] Creating role: %s", cfg.LambdaRoleName)
	trustPolicy := buildTrustPolicy()
	trustJSON, _ := json.Marshal(trustPolicy)

	createInput := &iam.CreateRoleInput{
		RoleName:                 aws.String(cfg.LambdaRoleName),
		AssumeRolePolicyDocument: aws.String(string(trustJSON)),
		Description:              aws.String("IAM role for IaC Lambda function"),
		Tags: []types.Tag{
			{Key: aws.String("Project"), Value: aws.String("iac-go")},
			{Key: aws.String("ManagedBy"), Value: aws.String("go-sdk")},
		},
	}
	// Attach permissions boundary if required by the account policy
	if cfg.PermissionsBoundaryARN != "" {
		log.Printf("[iam] Attaching permissions boundary: %s", cfg.PermissionsBoundaryARN)
		createInput.PermissionsBoundary = aws.String(cfg.PermissionsBoundaryARN)
	}
	createOut, err := client.CreateRole(ctx, createInput)
	if err != nil {
		return "", fmt.Errorf("CreateRole: %w", err)
	}
	roleARN = aws.ToString(createOut.Role.Arn)
	log.Printf("[iam] Role created: %s", roleARN)

	// 3. Attach managed policy: basic Lambda execution (CloudWatch Logs)
	_, err = client.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
		RoleName:  aws.String(cfg.LambdaRoleName),
		PolicyArn: aws.String("arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"),
	})
	if err != nil {
		return "", fmt.Errorf("AttachRolePolicy (basic): %w", err)
	}

	// 4. Attach inline policy: DynamoDB + CloudWatch custom metrics
	inlinePolicy := buildInlinePolicy(cfg.DynamoTableName, cfg.AWSRegion)
	inlineJSON, _ := json.Marshal(inlinePolicy)
	_, err = client.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(cfg.LambdaRoleName),
		PolicyName:     aws.String("iac-lambda-permissions"),
		PolicyDocument: aws.String(string(inlineJSON)),
	})
	if err != nil {
		return "", fmt.Errorf("PutRolePolicy: %w", err)
	}
	log.Printf("[iam] Policies attached to role %s", cfg.LambdaRoleName)

	// 5. Wait for IAM propagation (~10s minimum)
	log.Printf("[iam] Waiting for IAM propagation...")
	err = utils.PollUntil(60*time.Second, 5*time.Second, func() (bool, error) {
		_, e := getRoleARN(ctx, client, cfg.LambdaRoleName)
		return e == nil, nil
	})
	if err != nil {
		return "", fmt.Errorf("IAM propagation timeout: %w", err)
	}
	// Extra 10s buffer — IAM can be slow to propagate for AssumeRole
	time.Sleep(10 * time.Second)

	return roleARN, nil
}

// getRoleARN returns the ARN of an existing role or an error if not found.
func getRoleARN(ctx context.Context, client *iam.Client, roleName string) (string, error) {
	out, err := client.GetRole(ctx, &iam.GetRoleInput{
		RoleName: aws.String(roleName),
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.Role.Arn), nil
}

// buildTrustPolicy builds the trust policy document allowing Lambda to assume the role.
func buildTrustPolicy() map[string]any {
	return map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Effect": "Allow",
				"Principal": map[string]string{
					"Service": "lambda.amazonaws.com",
				},
				"Action": "sts:AssumeRole",
			},
		},
	}
}

// buildInlinePolicy builds an inline policy granting DynamoDB and CloudWatch access.
func buildInlinePolicy(tableName, region string) map[string]any {
	// Wildcard on account ID since it is not known at provisioning time
	tableARN := fmt.Sprintf("arn:aws:dynamodb:%s:*:table/%s", region, tableName)

	return map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Effect": "Allow",
				"Action": []string{
					"dynamodb:PutItem",
					"dynamodb:GetItem",
					"dynamodb:BatchWriteItem",
					"dynamodb:DescribeTable",
					"dynamodb:Query",
					"dynamodb:Scan",
				},
				"Resource": tableARN,
			},
			{
				"Effect": "Allow",
				"Action": []string{
					"cloudwatch:PutMetricData",
				},
				"Resource": "*",
			},
		},
	}
}

// DeleteIAMRole removes the IAM role and all its attached policies (cleanup helper).
func DeleteIAMRole(ctx context.Context, awsCfg aws.Config, cfg *config.Config) error {
	client := iam.NewFromConfig(awsCfg)

	// Detach managed policies first
	policies := []string{"arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"}
	for _, p := range policies {
		client.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{ //nolint
			RoleName:  aws.String(cfg.LambdaRoleName),
			PolicyArn: aws.String(p),
		})
	}

	// Delete inline policies
	listOut, err := client.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{
		RoleName: aws.String(cfg.LambdaRoleName),
	})
	if err == nil {
		for _, pName := range listOut.PolicyNames {
			client.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{ //nolint
				RoleName:   aws.String(cfg.LambdaRoleName),
				PolicyName: aws.String(pName),
			})
		}
	}

	_, err = client.DeleteRole(ctx, &iam.DeleteRoleInput{
		RoleName: aws.String(cfg.LambdaRoleName),
	})
	if err != nil && !strings.Contains(err.Error(), "NoSuchEntity") {
		return fmt.Errorf("DeleteRole: %w", err)
	}
	log.Printf("[iam] Role %s deleted", cfg.LambdaRoleName)
	return nil
}