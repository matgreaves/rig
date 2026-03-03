// Package sqsx provides an SQS client built on rig endpoints.
//
// In tests, construct from a resolved environment endpoint:
//
//	client := sqsx.Connect(env.Endpoint("queue"))
//	// use client for SQS operations against the test queue
//
// In service code, construct from parsed wiring:
//
//	w, _ := connect.ParseWiring(ctx)
//	client := sqsx.Connect(w.Egress("queue"))
package sqsx

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/matgreaves/rig/connect"
)

// URL extracts the SQS_ENDPOINT attribute from the endpoint.
func URL(ep connect.Endpoint) string {
	v, _ := connect.SQSEndpoint.Get(ep)
	return v
}

// QueueURL extracts the SQS_QUEUE_URL attribute from the endpoint.
func QueueURL(ep connect.Endpoint) string {
	v, _ := connect.SQSQueueURL.Get(ep)
	return v
}

// Connect creates an SQS client from a rig endpoint.
// It reads SQS_ENDPOINT, AWS_ACCESS_KEY_ID, and AWS_SECRET_ACCESS_KEY from
// the endpoint attributes.
func Connect(ep connect.Endpoint) *sqs.Client {
	endpoint, _ := connect.SQSEndpoint.Get(ep)
	accessKey, _ := connect.S3AccessKeyID.Get(ep)
	secretKey, _ := connect.S3SecretAccessKey.Get(ep)

	return sqs.New(sqs.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
	})
}
