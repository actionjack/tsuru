// Copyright 2015 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ec2

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/awslabs/aws-sdk-go/aws"
	"github.com/awslabs/aws-sdk-go/gen/ec2"
	"github.com/tsuru/tsuru/iaas"
	"github.com/tsuru/tsuru/log"
)

const defaultRegion = "us-east-1"

func init() {
	iaas.RegisterIaasProvider("ec2", NewEC2IaaS())
}

type EC2IaaS struct {
	base iaas.UserDataIaaS
}

func NewEC2IaaS() *EC2IaaS {
	return &EC2IaaS{base: iaas.UserDataIaaS{NamedIaaS: iaas.NamedIaaS{BaseIaaSName: "ec2"}}}
}

func (i *EC2IaaS) createEC2Handler(region string) (*ec2.EC2, error) {
	keyId, err := i.base.GetConfigString("key-id")
	if err != nil {
		return nil, err
	}
	secretKey, err := i.base.GetConfigString("secret-key")
	if err != nil {
		return nil, err
	}
	securityToken, _ := i.base.GetConfigString("security-token")
	provider := aws.DetectCreds(keyId, secretKey, securityToken)
	return ec2.New(provider, region, http.DefaultClient), nil
}

func (i *EC2IaaS) waitForDnsName(ec2Handler *ec2.EC2, instance *ec2.Instance) (*ec2.Instance, error) {
	t0 := time.Now()
	for instance.PublicDNSName == nil || *instance.PublicDNSName == "" {
		rawWait, _ := i.base.GetConfigString("wait-timeout")
		maxWaitTime, _ := strconv.Atoi(rawWait)
		if maxWaitTime == 0 {
			maxWaitTime = 300
		}
		instId := *instance.InstanceID
		if time.Now().Sub(t0) > time.Duration(maxWaitTime)*time.Second {
			return nil, fmt.Errorf("ec2: time out waiting for instance %s to start", instId)
		}
		log.Debugf("ec2: waiting for dnsname for instance %s", instId)
		time.Sleep(500 * time.Millisecond)
		req := ec2.DescribeInstancesRequest{InstanceIDs: []string{instId}}
		resp, err := ec2Handler.DescribeInstances(&req)
		if err != nil {
			return nil, err
		}
		if len(resp.Reservations) == 0 || len(resp.Reservations[0].Instances) == 0 {
			return nil, fmt.Errorf("No instances returned")
		}
		instance = &resp.Reservations[0].Instances[0]
	}
	return instance, nil
}

func (i *EC2IaaS) Describe() string {
	return `EC2 IaaS required params:
  image=<image id>         Image AMI ID
  type=<instance type>     Your template uuid

Optional params:
  region=<region>          Chosen region, defaults to us-east-1
  securityGroup=<group>    Chosen security group
  keyName=<key name>       Key name for machine
`
}

func (i *EC2IaaS) Clone(name string) iaas.IaaS {
	clone := *i
	clone.base.IaaSName = name
	return &clone
}

func (i *EC2IaaS) DeleteMachine(m *iaas.Machine) error {
	region, ok := m.CreationParams["region"]
	if !ok {
		return fmt.Errorf("region creation param required")
	}
	ec2Handler, err := i.createEC2Handler(region)
	if err != nil {
		return err
	}
	req := ec2.TerminateInstancesRequest{InstanceIDs: []string{m.Id}}
	_, err = ec2Handler.TerminateInstances(&req)
	return err
}

func (i *EC2IaaS) CreateMachine(params map[string]string) (*iaas.Machine, error) {
	if _, ok := params["region"]; !ok {
		params["region"] = defaultRegion
	}
	region := params["region"]
	imageId, ok := params["image"]
	if !ok {
		return nil, fmt.Errorf("image param required")
	}
	instanceType, ok := params["type"]
	if !ok {
		return nil, fmt.Errorf("type param required")
	}
	optimized, _ := params["ebs-optimized"]
	ebsOptimized, _ := strconv.ParseBool(optimized)
	userData, err := i.base.ReadUserData()
	if err != nil {
		return nil, err
	}
	keyName, _ := params["keyName"]
	options := ec2.RunInstancesRequest{
		EBSOptimized: aws.Boolean(ebsOptimized),
		ImageID:      aws.String(imageId),
		InstanceType: aws.String(instanceType),
		UserData:     aws.String(userData),
		MinCount:     aws.Integer(1),
		MaxCount:     aws.Integer(1),
		KeyName:      aws.String(keyName),
	}
	securityGroup, ok := params["securityGroup"]
	if ok {
		options.SecurityGroups = []string{securityGroup}
	}
	ec2Handler, err := i.createEC2Handler(region)
	if err != nil {
		return nil, err
	}
	resp, err := ec2Handler.RunInstances(&options)
	if err != nil {
		return nil, err
	}
	if len(resp.Instances) == 0 {
		return nil, fmt.Errorf("no instance created")
	}
	runInst := &resp.Instances[0]
	instance, err := i.waitForDnsName(ec2Handler, runInst)
	if err != nil {
		req := ec2.TerminateInstancesRequest{InstanceIDs: []string{*runInst.InstanceID}}
		ec2Handler.TerminateInstances(&req)
		return nil, err
	}
	machine := iaas.Machine{
		Id:      *instance.InstanceID,
		Status:  *instance.State.Name,
		Address: *instance.PublicDNSName,
	}
	return &machine, nil
}
