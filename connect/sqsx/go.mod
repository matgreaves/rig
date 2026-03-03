module github.com/matgreaves/rig/connect/sqsx

go 1.25.5

require (
	github.com/aws/aws-sdk-go-v2 v1.36.3
	github.com/aws/aws-sdk-go-v2/credentials v1.17.67
	github.com/aws/aws-sdk-go-v2/service/sqs v1.38.5
	github.com/matgreaves/rig v0.0.0
)

require (
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.3.34 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.6.34 // indirect
	github.com/aws/smithy-go v1.22.2 // indirect
)

replace github.com/matgreaves/rig => ../../
