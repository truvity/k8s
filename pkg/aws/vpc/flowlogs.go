package vpc

import (
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	defaultRetentionDays      = 365
	defaultAggregationSeconds = 60
	defaultTrafficType        = "ALL"

	flowLogsAssumeRolePolicy = `{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Service": "vpc-flow-logs.amazonaws.com"},
    "Action": "sts:AssumeRole"
  }]
}`
)

func orDefault[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}

	return v
}

// createFlowLogs registers the log group, the role and its policy, and the
// flow log. The role may write only to the log group; the two Describe
// actions need "*" because AWS has no narrower resource for a list.
func (b *builder) createFlowLogs() error {
	a := b.args
	f := a.FlowLogs

	if f.Disable {
		return nil
	}

	if f.Destination == FlowLogsS3 {
		return b.createS3FlowLogs()
	}

	groupChild := Child{Kind: KindFlowLogGroup}

	group, err := cloudwatch.NewLogGroup(b.ctx, b.child(groupChild), &cloudwatch.LogGroupArgs{
		Name:            pulumi.String(orDefault(f.LogGroupName, "/vpc/flow-logs/"+b.name)),
		RetentionInDays: pulumi.Int(orDefault(f.RetentionDays, defaultRetentionDays)),
		Tags:            b.tags(groupChild, nil, nil),
	}, b.childOpts(groupOpts(f)...)...)
	if err != nil {
		return fmt.Errorf("create flow logs log group: %w", err)
	}

	roleChild := Child{Kind: KindFlowLogRole}
	roleArgs := &iam.RoleArgs{
		Name:             pulumi.String(orDefault(f.RoleName, b.name+"-flow-logs")),
		AssumeRolePolicy: pulumi.String(flowLogsAssumeRolePolicy),
		Tags:             b.tags(roleChild, nil, nil),
	}

	if f.PermissionsBoundaryARN != nil {
		roleArgs.PermissionsBoundary = f.PermissionsBoundaryARN
	}

	role, err := iam.NewRole(b.ctx, b.child(roleChild), roleArgs, b.childOpts()...)
	if err != nil {
		return fmt.Errorf("create flow logs role: %w", err)
	}

	policy := pulumi.Sprintf(`{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "logs:DescribeLogGroups",
        "logs:DescribeLogStreams"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "logs:CreateLogGroup",
        "logs:CreateLogStream",
        "logs:PutLogEvents"
      ],
      "Resource": [
        "%s",
        "%s:*"
      ]
    }
  ]
}`, group.Arn, group.Arn)

	if _, err := iam.NewRolePolicy(b.ctx, b.child(Child{Kind: KindFlowLogPolicy}), &iam.RolePolicyArgs{
		Role:   role.Name,
		Policy: policy,
	}, b.childOpts()...); err != nil {
		return fmt.Errorf("create flow logs policy: %w", err)
	}

	flowChild := Child{Kind: KindFlowLog}

	fl, err := ec2.NewFlowLog(b.ctx, b.child(flowChild), &ec2.FlowLogArgs{
		VpcId:                  b.vpc.ID(),
		TrafficType:            pulumi.String(orDefault(f.TrafficType, defaultTrafficType)),
		LogDestinationType:     pulumi.String("cloud-watch-logs"),
		LogDestination:         group.Arn,
		IamRoleArn:             role.Arn,
		MaxAggregationInterval: pulumi.Int(orDefault(f.AggregationSeconds, defaultAggregationSeconds)),
		Tags:                   b.tags(flowChild, nil, nil),
	}, b.childOpts()...)
	if err != nil {
		return fmt.Errorf("create flow log: %w", err)
	}

	b.comp.FlowLogID = fl.ID().ToStringOutput()

	return nil
}

func groupOpts(f FlowLogs) []pulumi.ResourceOption {
	if f.RetainLogGroup {
		return []pulumi.ResourceOption{pulumi.RetainOnDelete(true)}
	}

	return nil
}

// createS3FlowLogs registers the bucket (private, SSE-S3, TLS only, expiring)
// and the flow log that delivers to it. The default log format is used.
func (b *builder) createS3FlowLogs() error {
	a := b.args
	f := a.FlowLogs
	s := f.S3
	partition := orDefault(s.Partition, "aws")
	days := orDefault(s.ExpireDays, 90)

	bucketChild := Child{Kind: KindFlowLogBucket}

	bucket, err := s3.NewBucketV2(b.ctx, b.child(bucketChild), &s3.BucketV2Args{
		Bucket: pulumi.String(s.BucketName),
		Tags:   b.tags(bucketChild, nil, nil),
	}, b.childOpts()...)
	if err != nil {
		return fmt.Errorf("create flow logs bucket: %w", err)
	}

	if _, err := s3.NewBucketPublicAccessBlock(b.ctx, b.child(Child{Kind: KindFlowLogBucketPublicAccess}), &s3.BucketPublicAccessBlockArgs{
		Bucket:                bucket.ID(),
		BlockPublicAcls:       pulumi.Bool(true),
		BlockPublicPolicy:     pulumi.Bool(true),
		IgnorePublicAcls:      pulumi.Bool(true),
		RestrictPublicBuckets: pulumi.Bool(true),
	}, b.childOpts()...); err != nil {
		return fmt.Errorf("create flow logs bucket public access block: %w", err)
	}

	if _, err := s3.NewBucketServerSideEncryptionConfigurationV2(b.ctx, b.child(Child{Kind: KindFlowLogBucketEncryption}), &s3.BucketServerSideEncryptionConfigurationV2Args{
		Bucket: bucket.ID(),
		Rules: s3.BucketServerSideEncryptionConfigurationV2RuleArray{
			&s3.BucketServerSideEncryptionConfigurationV2RuleArgs{
				ApplyServerSideEncryptionByDefault: &s3.BucketServerSideEncryptionConfigurationV2RuleApplyServerSideEncryptionByDefaultArgs{
					SseAlgorithm: pulumi.String("AES256"),
				},
			},
		},
	}, b.childOpts()...); err != nil {
		return fmt.Errorf("create flow logs bucket encryption: %w", err)
	}

	policy := pulumi.Sprintf(`{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "AWSLogDeliveryWrite",
      "Effect": "Allow",
      "Principal": {"Service": "delivery.logs.amazonaws.com"},
      "Action": "s3:PutObject",
      "Resource": "%[1]s/AWSLogs/%[2]s/*",
      "Condition": {
        "StringEquals": {"aws:SourceAccount": "%[2]s", "s3:x-amz-acl": "bucket-owner-full-control"},
        "ArnLike": {"aws:SourceArn": "arn:%[3]s:logs:*:%[2]s:*"}
      }
    },
    {
      "Sid": "AWSLogDeliveryAclCheck",
      "Effect": "Allow",
      "Principal": {"Service": "delivery.logs.amazonaws.com"},
      "Action": "s3:GetBucketAcl",
      "Resource": "%[1]s",
      "Condition": {
        "StringEquals": {"aws:SourceAccount": "%[2]s"},
        "ArnLike": {"aws:SourceArn": "arn:%[3]s:logs:*:%[2]s:*"}
      }
    },
    {
      "Sid": "DenyInsecureTransport",
      "Effect": "Deny",
      "Principal": "*",
      "Action": "s3:*",
      "Resource": ["%[1]s", "%[1]s/*"],
      "Condition": {"Bool": {"aws:SecureTransport": "false"}}
    }
  ]
}`, bucket.Arn, s.AccountID, partition)

	pol, err := s3.NewBucketPolicy(b.ctx, b.child(Child{Kind: KindFlowLogBucketPolicy}), &s3.BucketPolicyArgs{
		Bucket: bucket.ID(),
		Policy: policy,
	}, b.childOpts()...)
	if err != nil {
		return fmt.Errorf("create flow logs bucket policy: %w", err)
	}

	if _, err := s3.NewBucketLifecycleConfigurationV2(b.ctx, b.child(Child{Kind: KindFlowLogBucketLifecycle}), &s3.BucketLifecycleConfigurationV2Args{
		Bucket: bucket.ID(),
		Rules: s3.BucketLifecycleConfigurationV2RuleArray{
			&s3.BucketLifecycleConfigurationV2RuleArgs{
				Id:     pulumi.String("expire"),
				Status: pulumi.String("Enabled"),
				Filter: &s3.BucketLifecycleConfigurationV2RuleFilterArgs{},
				Expiration: &s3.BucketLifecycleConfigurationV2RuleExpirationArgs{
					Days: pulumi.Int(days),
				},
				AbortIncompleteMultipartUpload: &s3.BucketLifecycleConfigurationV2RuleAbortIncompleteMultipartUploadArgs{
					DaysAfterInitiation: pulumi.Int(7),
				},
			},
		},
	}, b.childOpts()...); err != nil {
		return fmt.Errorf("create flow logs bucket lifecycle: %w", err)
	}

	flowChild := Child{Kind: KindFlowLog}

	fl, err := ec2.NewFlowLog(b.ctx, b.child(flowChild), &ec2.FlowLogArgs{
		VpcId:                  b.vpc.ID(),
		TrafficType:            pulumi.String(orDefault(f.TrafficType, defaultTrafficType)),
		LogDestinationType:     pulumi.String("s3"),
		LogDestination:         bucket.Arn,
		MaxAggregationInterval: pulumi.Int(orDefault(f.AggregationSeconds, defaultAggregationSeconds)),
		Tags:                   b.tags(flowChild, nil, nil),
	}, b.childOpts(pulumi.DependsOn([]pulumi.Resource{pol}))...)
	if err != nil {
		return fmt.Errorf("create flow log: %w", err)
	}

	b.comp.FlowLogID = fl.ID().ToStringOutput()

	return nil
}
