// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"errors"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/copilot-cli/internal/pkg/template"
	"github.com/imdario/mergo"
)

const (
	lbWebSvcManifestPath = "workloads/services/lb-web/manifest.yml"
)

// Default values for HTTPHealthCheck for a load balanced web service.
const (
	DefaultHealthCheckPath        = "/"
	DefaultHealthCheckGracePeriod = 60
)

var (
	errUnmarshalHealthCheckArgs = errors.New("can't unmarshal healthcheck field into string or compose-style map")
)

// durationp is a utility function used to convert a time.Duration to a pointer. Useful for YAML unmarshaling
// and template execution.
func durationp(v time.Duration) *time.Duration {
	return &v
}

// LoadBalancedWebService holds the configuration to build a container image with an exposed port that receives
// requests through a load balancer with AWS Fargate as the compute engine.
type LoadBalancedWebService struct {
	Workload                     `yaml:",inline"`
	LoadBalancedWebServiceConfig `yaml:",inline"`
	// Use *LoadBalancedWebServiceConfig because of https://github.com/imdario/mergo/issues/146
	Environments map[string]*LoadBalancedWebServiceConfig `yaml:",flow"` // Fields to override per environment.

	parser template.Parser
}

// LoadBalancedWebServiceConfig holds the configuration for a load balanced web service.
type LoadBalancedWebServiceConfig struct {
	ImageConfig      ImageWithPortAndHealthcheck `yaml:"image,flow"`
	ImageOverride    `yaml:",inline"`
	RoutingRule      `yaml:"http,flow"`
	TaskConfig       `yaml:",inline"`
	*Logging         `yaml:"logging,flow"`
	Sidecars         map[string]*SidecarConfig `yaml:"sidecars"`
	Network          *NetworkConfig            `yaml:"network"` // TODO: the type needs to be updated after we upgrade mergo
	Publish          *PublishConfig            `yaml:"publish"`
	TaskDefOverrides []OverrideRule            `yaml:"taskdef_overrides"`
}

// LoadBalancedWebServiceProps contains properties for creating a new load balanced fargate service manifest.
type LoadBalancedWebServiceProps struct {
	*WorkloadProps
	Path        string
	Port        uint16
	HealthCheck *ContainerHealthCheck // Optional healthcheck configuration.
}

// NewLoadBalancedWebService creates a new public load balanced web service, receives all the requests from the load balancer,
// has a single task with minimal CPU and memory thresholds, and sets the default health check path to "/".
func NewLoadBalancedWebService(props *LoadBalancedWebServiceProps) *LoadBalancedWebService {
	svc := newDefaultLoadBalancedWebService()
	// Apply overrides.
	svc.Name = stringP(props.Name)
	svc.LoadBalancedWebServiceConfig.ImageConfig.Image.Location = stringP(props.Image)
	svc.LoadBalancedWebServiceConfig.ImageConfig.Build.BuildArgs.Dockerfile = stringP(props.Dockerfile)
	svc.LoadBalancedWebServiceConfig.ImageConfig.Port = aws.Uint16(props.Port)
	svc.LoadBalancedWebServiceConfig.ImageConfig.HealthCheck = props.HealthCheck
	svc.RoutingRule.Path = aws.String(props.Path)
	svc.parser = template.New()
	return svc
}

// newDefaultLoadBalancedWebService returns an empty LoadBalancedWebService with only the default values set.
func newDefaultLoadBalancedWebService() *LoadBalancedWebService {
	return &LoadBalancedWebService{
		Workload: Workload{
			Type: aws.String(LoadBalancedWebServiceType),
		},
		LoadBalancedWebServiceConfig: LoadBalancedWebServiceConfig{
			ImageConfig: ImageWithPortAndHealthcheck{},
			RoutingRule: RoutingRule{
				HealthCheck: HealthCheckArgsOrString{
					HealthCheckPath: aws.String(DefaultHealthCheckPath),
				},
			},
			TaskConfig: TaskConfig{
				CPU:    aws.Int(256),
				Memory: aws.Int(512),
				Count: Count{
					Value: aws.Int(1),
				},
				ExecuteCommand: ExecuteCommand{
					Enable: aws.Bool(false),
				},
			},
			Network: &NetworkConfig{
				VPC: &vpcConfig{
					Placement: stringP(PublicSubnetPlacement),
				},
			},
		},
	}
}

// MarshalBinary serializes the manifest object into a binary YAML document.
// Implements the encoding.BinaryMarshaler interface.
func (s *LoadBalancedWebService) MarshalBinary() ([]byte, error) {
	content, err := s.parser.Parse(lbWebSvcManifestPath, *s)
	if err != nil {
		return nil, err
	}
	return content.Bytes(), nil
}

// Port returns the exposed port in the manifest.
// A LoadBalancedWebService always has a port exposed therefore the boolean is always true.
func (s *LoadBalancedWebService) Port() (port uint16, ok bool) {
	return aws.Uint16Value(s.ImageConfig.Port), true
}

// Publish returns the list of topics where notifications can be published.
func (s *LoadBalancedWebService) Publish() []Topic {
	if s.LoadBalancedWebServiceConfig.Publish == nil {
		return nil
	}
	return s.LoadBalancedWebServiceConfig.Publish.Topics
}

// BuildRequired returns if the service requires building from the local Dockerfile.
func (s *LoadBalancedWebService) BuildRequired() (bool, error) {
	return requiresBuild(s.ImageConfig.Image)
}

// BuildArgs returns a docker.BuildArguments object given a ws root directory.
func (s *LoadBalancedWebService) BuildArgs(wsRoot string) *DockerBuildArgs {
	return s.ImageConfig.BuildConfig(wsRoot)
}

// ApplyEnv returns the service manifest with environment overrides.
// If the environment passed in does not have any overrides then it returns itself.
func (s LoadBalancedWebService) ApplyEnv(envName string) (WorkloadManifest, error) {
	overrideConfig, ok := s.Environments[envName]
	if !ok {
		return &s, nil
	}

	if overrideConfig == nil {
		return &s, nil
	}

	for _, t := range defaultTransformers {
		// Apply overrides to the original service s.
		err := mergo.Merge(&s, LoadBalancedWebService{
			LoadBalancedWebServiceConfig: *overrideConfig,
		}, mergo.WithOverride, mergo.WithTransformers(t))

		if err != nil {
			return nil, err
		}
	}

	s.Environments = nil
	return &s, nil
}

// RoutingRule holds the path to route requests to the service.
type RoutingRule struct {
	Path                *string                 `yaml:"path"`
	HealthCheck         HealthCheckArgsOrString `yaml:"healthcheck"`
	Stickiness          *bool                   `yaml:"stickiness"`
	Alias               *Alias                  `yaml:"alias"`
	DeregistrationDelay *time.Duration          `yaml:"deregistration_delay"`
	// TargetContainer is the container load balancer routes traffic to.
	TargetContainer          *string   `yaml:"target_container"`
	TargetContainerCamelCase *string   `yaml:"targetContainer"`    // "targetContainerCamelCase" for backwards compatibility
	AllowedSourceIps         *[]string `yaml:"allowed_source_ips"` // TODO: the type needs to be updated after we upgrade mergo
}

// Alias is a custom type which supports unmarshaling "http.alias" yaml which
// can either be of type string or type slice of string.
type Alias stringSliceOrString

// UnmarshalYAML overrides the default YAML unmarshaling logic for the Alias
// struct, allowing it to perform more complex unmarshaling behavior.
// This method implements the yaml.Unmarshaler (v2) interface.
func (e *Alias) UnmarshalYAML(unmarshal func(interface{}) error) error {
	if err := unmarshalYAMLToStringSliceOrString((*stringSliceOrString)(e), unmarshal); err != nil {
		return errUnmarshalEntryPoint
	}
	return nil
}

// ToStringSlice converts an Alias to a slice of string using shell-style rules.
func (e *Alias) ToStringSlice() ([]string, error) {
	out, err := toStringSlice((*stringSliceOrString)(e))
	if err != nil {
		return nil, err
	}
	return out, nil
}
