// Package s3x provides an S3 client built on rig endpoints.
//
// In tests, construct from a resolved environment endpoint:
//
//	client := s3x.Connect(env.Endpoint("storage"))
//	// use client for S3 operations against the test bucket
//
// In service code, construct from parsed wiring:
//
//	w, _ := connect.ParseWiring(ctx)
//	client := s3x.Connect(w.Egress("storage"))
package s3x

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/matgreaves/rig/connect"
)

// URL extracts the S3_ENDPOINT attribute from the endpoint.
func URL(ep connect.Endpoint) string {
	v, _ := connect.S3Endpoint.Get(ep)
	return v
}

// Bucket extracts the S3_BUCKET attribute from the endpoint.
func Bucket(ep connect.Endpoint) string {
	v, _ := connect.S3Bucket.Get(ep)
	return v
}

// Connect creates an S3 client from a rig endpoint.
// It reads S3_ENDPOINT, AWS_ACCESS_KEY_ID, and AWS_SECRET_ACCESS_KEY from
// the endpoint attributes. When S3_ENDPOINT is set (custom backend like
// MinIO or similar), path-style access is enabled automatically since
// virtual-hosted addressing requires wildcard DNS.
func Connect(ep connect.Endpoint) *s3.Client {
	endpoint, hasEndpoint := connect.S3Endpoint.Get(ep)
	accessKey, _ := connect.S3AccessKeyID.Get(ep)
	secretKey, _ := connect.S3SecretAccessKey.Get(ep)

	opts := s3.Options{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
	}
	if hasEndpoint {
		opts.BaseEndpoint = aws.String(endpoint)
		opts.UsePathStyle = true
	}

	return s3.New(opts)
}
