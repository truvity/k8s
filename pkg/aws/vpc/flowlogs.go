package vpc

import (
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
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

	groupChild := Child{Kind: KindFlowLogGroup}

	group, err := cloudwatch.NewLogGroup(b.ctx, b.child(groupChild), &cloudwatch.LogGroupArgs{
		Name:            pulumi.String(orDefault(f.LogGroupName, "/vpc/flow-logs/"+b.name)),
		RetentionInDays: pulumi.Int(orDefault(f.RetentionDays, defaultRetentionDays)),
		Tags:            b.tags(groupChild, nil, nil),
	}, b.childOpts()...)
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
