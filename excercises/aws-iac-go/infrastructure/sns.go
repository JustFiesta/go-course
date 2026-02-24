package infrastructure

import (
	"context"
	"fmt"
	"log"
	"strings"

	"aws-iac-go/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snsTypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
)

// SetupSNS creates an SNS topic for alerts and optionally subscribes an email address.
// Returns the topic ARN. CreateTopic is idempotent — safe to call multiple times.
func SetupSNS(ctx context.Context, awsCfg aws.Config, cfg *config.Config) (string, error) {
	client := sns.NewFromConfig(awsCfg)

	// CreateTopic is idempotent — returns the existing ARN if the topic already exists
	log.Printf("[sns] Creating/verifying topic: %s", cfg.SNSTopicName)
	out, err := client.CreateTopic(ctx, &sns.CreateTopicInput{
		Name: aws.String(cfg.SNSTopicName),
		Tags: []snsTypes.Tag{
			{Key: aws.String("Project"),   Value: aws.String("iac-go")},
			{Key: aws.String("ManagedBy"), Value: aws.String("go-sdk")},
		},
	})
	if err != nil {
		return "", fmt.Errorf("CreateTopic: %w", err)
	}
	topicARN := aws.ToString(out.TopicArn)
	log.Printf("[sns] Topic ready: %s", topicARN)

	// Optional email subscription
	if cfg.AlertEmail != "" {
		log.Printf("[sns] Adding email subscription: %s", cfg.AlertEmail)
		_, err = client.Subscribe(ctx, &sns.SubscribeInput{
			TopicArn: aws.String(topicARN),
			Protocol: aws.String("email"),
			Endpoint: aws.String(cfg.AlertEmail),
		})
		if err != nil {
			// Non-fatal — email subscription is optional
			log.Printf("[sns] WARN: Could not subscribe email: %v", err)
		} else {
			log.Printf("[sns] Email subscription added (confirmation required)")
		}
	}

	return topicARN, nil
}

// DeleteSNSTopic removes the SNS topic (cleanup helper).
func DeleteSNSTopic(ctx context.Context, awsCfg aws.Config, topicARN string) error {
	client := sns.NewFromConfig(awsCfg)
	_, err := client.DeleteTopic(ctx, &sns.DeleteTopicInput{
		TopicArn: aws.String(topicARN),
	})
	if err != nil && !strings.Contains(err.Error(), "NotFound") {
		return fmt.Errorf("DeleteTopic: %w", err)
	}
	log.Printf("[sns] Topic %s deleted", topicARN)
	return nil
}