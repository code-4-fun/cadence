// Copyright (c) 2019 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package frontend

import (
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/uber/cadence/common"
	"github.com/uber/cadence/common/client"
	"github.com/uber/cadence/common/domain"
	"github.com/uber/cadence/common/dynamicconfig"
	"github.com/uber/cadence/common/resource"
	"github.com/uber/cadence/common/service"
)

// Config represents configuration for cadence-frontend service
type Config struct {
	NumHistoryShards                int
	IsAdvancedVisConfigExist        bool
	domainConfig                    domain.Config
	PersistenceMaxQPS               dynamicconfig.IntPropertyFn
	PersistenceGlobalMaxQPS         dynamicconfig.IntPropertyFn
	VisibilityMaxPageSize           dynamicconfig.IntPropertyFnWithDomainFilter
	EnableVisibilitySampling        dynamicconfig.BoolPropertyFn
	EnableReadFromClosedExecutionV2 dynamicconfig.BoolPropertyFn
	// deprecated: never used for ratelimiting, only sampling-based failure injection, and only on database-based visibility
	VisibilityListMaxQPS       dynamicconfig.IntPropertyFnWithDomainFilter
	EnableReadVisibilityFromES dynamicconfig.BoolPropertyFnWithDomainFilter
	// deprecated: never read from
	ESVisibilityListMaxQPS            dynamicconfig.IntPropertyFnWithDomainFilter
	ESIndexMaxResultWindow            dynamicconfig.IntPropertyFn
	HistoryMaxPageSize                dynamicconfig.IntPropertyFnWithDomainFilter
	UserRPS                           dynamicconfig.IntPropertyFn
	WorkerRPS                         dynamicconfig.IntPropertyFn
	VisibilityRPS                     dynamicconfig.IntPropertyFn
	MaxDomainUserRPSPerInstance       dynamicconfig.IntPropertyFnWithDomainFilter
	MaxDomainWorkerRPSPerInstance     dynamicconfig.IntPropertyFnWithDomainFilter
	MaxDomainVisibilityRPSPerInstance dynamicconfig.IntPropertyFnWithDomainFilter
	GlobalDomainUserRPS               dynamicconfig.IntPropertyFnWithDomainFilter
	GlobalDomainWorkerRPS             dynamicconfig.IntPropertyFnWithDomainFilter
	GlobalDomainVisibilityRPS         dynamicconfig.IntPropertyFnWithDomainFilter
	EnableClientVersionCheck          dynamicconfig.BoolPropertyFn
	DisallowQuery                     dynamicconfig.BoolPropertyFnWithDomainFilter
	ShutdownDrainDuration             dynamicconfig.DurationPropertyFn
	Lockdown                          dynamicconfig.BoolPropertyFnWithDomainFilter

	// id length limits
	MaxIDLengthWarnLimit  dynamicconfig.IntPropertyFn
	DomainNameMaxLength   dynamicconfig.IntPropertyFnWithDomainFilter
	IdentityMaxLength     dynamicconfig.IntPropertyFnWithDomainFilter
	WorkflowIDMaxLength   dynamicconfig.IntPropertyFnWithDomainFilter
	SignalNameMaxLength   dynamicconfig.IntPropertyFnWithDomainFilter
	WorkflowTypeMaxLength dynamicconfig.IntPropertyFnWithDomainFilter
	RequestIDMaxLength    dynamicconfig.IntPropertyFnWithDomainFilter
	TaskListNameMaxLength dynamicconfig.IntPropertyFnWithDomainFilter

	// Persistence settings
	HistoryMgrNumConns dynamicconfig.IntPropertyFn

	// security protection settings
	EnableAdminProtection         dynamicconfig.BoolPropertyFn
	AdminOperationToken           dynamicconfig.StringPropertyFn
	DisableListVisibilityByFilter dynamicconfig.BoolPropertyFnWithDomainFilter

	// size limit system protection
	BlobSizeLimitError dynamicconfig.IntPropertyFnWithDomainFilter
	BlobSizeLimitWarn  dynamicconfig.IntPropertyFnWithDomainFilter

	ThrottledLogRPS dynamicconfig.IntPropertyFn

	// Domain specific config
	EnableDomainNotActiveAutoForwarding         dynamicconfig.BoolPropertyFnWithDomainFilter
	EnableGracefulFailover                      dynamicconfig.BoolPropertyFn
	DomainFailoverRefreshInterval               dynamicconfig.DurationPropertyFn
	DomainFailoverRefreshTimerJitterCoefficient dynamicconfig.FloatPropertyFn

	// ValidSearchAttributes is legal indexed keys that can be used in list APIs
	ValidSearchAttributes             dynamicconfig.MapPropertyFn
	SearchAttributesNumberOfKeysLimit dynamicconfig.IntPropertyFnWithDomainFilter
	SearchAttributesSizeOfValueLimit  dynamicconfig.IntPropertyFnWithDomainFilter
	SearchAttributesTotalSizeLimit    dynamicconfig.IntPropertyFnWithDomainFilter

	// VisibilityArchival system protection
	VisibilityArchivalQueryMaxPageSize dynamicconfig.IntPropertyFn

	SendRawWorkflowHistory dynamicconfig.BoolPropertyFnWithDomainFilter

	// max number of decisions per RespondDecisionTaskCompleted request (unlimited by default)
	DecisionResultCountLimit dynamicconfig.IntPropertyFnWithDomainFilter

	// Debugging

	// Emit signal related metrics with signal name tag. Be aware of cardinality.
	EmitSignalNameMetricsTag dynamicconfig.BoolPropertyFnWithDomainFilter
}

// NewConfig returns new service config with default values
func NewConfig(dc *dynamicconfig.Collection, numHistoryShards int, isAdvancedVisConfigExist bool) *Config {
	return &Config{
		NumHistoryShards:                            numHistoryShards,
		IsAdvancedVisConfigExist:                    isAdvancedVisConfigExist,
		PersistenceMaxQPS:                           dc.GetIntProperty(dynamicconfig.FrontendPersistenceMaxQPS),
		PersistenceGlobalMaxQPS:                     dc.GetIntProperty(dynamicconfig.FrontendPersistenceGlobalMaxQPS),
		VisibilityMaxPageSize:                       dc.GetIntPropertyFilteredByDomain(dynamicconfig.FrontendVisibilityMaxPageSize),
		EnableVisibilitySampling:                    dc.GetBoolProperty(dynamicconfig.EnableVisibilitySampling),
		EnableReadFromClosedExecutionV2:             dc.GetBoolProperty(dynamicconfig.EnableReadFromClosedExecutionV2),
		VisibilityListMaxQPS:                        dc.GetIntPropertyFilteredByDomain(dynamicconfig.FrontendVisibilityListMaxQPS),
		ESVisibilityListMaxQPS:                      dc.GetIntPropertyFilteredByDomain(dynamicconfig.FrontendESVisibilityListMaxQPS),
		EnableReadVisibilityFromES:                  dc.GetBoolPropertyFilteredByDomain(dynamicconfig.EnableReadVisibilityFromES),
		ESIndexMaxResultWindow:                      dc.GetIntProperty(dynamicconfig.FrontendESIndexMaxResultWindow),
		HistoryMaxPageSize:                          dc.GetIntPropertyFilteredByDomain(dynamicconfig.FrontendHistoryMaxPageSize),
		UserRPS:                                     dc.GetIntProperty(dynamicconfig.FrontendUserRPS),
		WorkerRPS:                                   dc.GetIntProperty(dynamicconfig.FrontendWorkerRPS),
		VisibilityRPS:                               dc.GetIntProperty(dynamicconfig.FrontendVisibilityRPS),
		MaxDomainUserRPSPerInstance:                 dc.GetIntPropertyFilteredByDomain(dynamicconfig.FrontendMaxDomainUserRPSPerInstance),
		MaxDomainWorkerRPSPerInstance:               dc.GetIntPropertyFilteredByDomain(dynamicconfig.FrontendMaxDomainWorkerRPSPerInstance),
		MaxDomainVisibilityRPSPerInstance:           dc.GetIntPropertyFilteredByDomain(dynamicconfig.FrontendMaxDomainVisibilityRPSPerInstance),
		GlobalDomainUserRPS:                         dc.GetIntPropertyFilteredByDomain(dynamicconfig.FrontendGlobalDomainUserRPS),
		GlobalDomainWorkerRPS:                       dc.GetIntPropertyFilteredByDomain(dynamicconfig.FrontendGlobalDomainWorkerRPS),
		GlobalDomainVisibilityRPS:                   dc.GetIntPropertyFilteredByDomain(dynamicconfig.FrontendGlobalDomainVisibilityRPS),
		MaxIDLengthWarnLimit:                        dc.GetIntProperty(dynamicconfig.MaxIDLengthWarnLimit),
		DomainNameMaxLength:                         dc.GetIntPropertyFilteredByDomain(dynamicconfig.DomainNameMaxLength),
		IdentityMaxLength:                           dc.GetIntPropertyFilteredByDomain(dynamicconfig.IdentityMaxLength),
		WorkflowIDMaxLength:                         dc.GetIntPropertyFilteredByDomain(dynamicconfig.WorkflowIDMaxLength),
		SignalNameMaxLength:                         dc.GetIntPropertyFilteredByDomain(dynamicconfig.SignalNameMaxLength),
		WorkflowTypeMaxLength:                       dc.GetIntPropertyFilteredByDomain(dynamicconfig.WorkflowTypeMaxLength),
		RequestIDMaxLength:                          dc.GetIntPropertyFilteredByDomain(dynamicconfig.RequestIDMaxLength),
		TaskListNameMaxLength:                       dc.GetIntPropertyFilteredByDomain(dynamicconfig.TaskListNameMaxLength),
		HistoryMgrNumConns:                          dc.GetIntProperty(dynamicconfig.FrontendHistoryMgrNumConns),
		EnableAdminProtection:                       dc.GetBoolProperty(dynamicconfig.EnableAdminProtection),
		AdminOperationToken:                         dc.GetStringProperty(dynamicconfig.AdminOperationToken),
		DisableListVisibilityByFilter:               dc.GetBoolPropertyFilteredByDomain(dynamicconfig.DisableListVisibilityByFilter),
		BlobSizeLimitError:                          dc.GetIntPropertyFilteredByDomain(dynamicconfig.BlobSizeLimitError),
		BlobSizeLimitWarn:                           dc.GetIntPropertyFilteredByDomain(dynamicconfig.BlobSizeLimitWarn),
		ThrottledLogRPS:                             dc.GetIntProperty(dynamicconfig.FrontendThrottledLogRPS),
		ShutdownDrainDuration:                       dc.GetDurationProperty(dynamicconfig.FrontendShutdownDrainDuration),
		EnableDomainNotActiveAutoForwarding:         dc.GetBoolPropertyFilteredByDomain(dynamicconfig.EnableDomainNotActiveAutoForwarding),
		EnableGracefulFailover:                      dc.GetBoolProperty(dynamicconfig.EnableGracefulFailover),
		DomainFailoverRefreshInterval:               dc.GetDurationProperty(dynamicconfig.DomainFailoverRefreshInterval),
		DomainFailoverRefreshTimerJitterCoefficient: dc.GetFloat64Property(dynamicconfig.DomainFailoverRefreshTimerJitterCoefficient),
		EnableClientVersionCheck:                    dc.GetBoolProperty(dynamicconfig.EnableClientVersionCheck),
		ValidSearchAttributes:                       dc.GetMapProperty(dynamicconfig.ValidSearchAttributes),
		SearchAttributesNumberOfKeysLimit:           dc.GetIntPropertyFilteredByDomain(dynamicconfig.SearchAttributesNumberOfKeysLimit),
		SearchAttributesSizeOfValueLimit:            dc.GetIntPropertyFilteredByDomain(dynamicconfig.SearchAttributesSizeOfValueLimit),
		SearchAttributesTotalSizeLimit:              dc.GetIntPropertyFilteredByDomain(dynamicconfig.SearchAttributesTotalSizeLimit),
		VisibilityArchivalQueryMaxPageSize:          dc.GetIntProperty(dynamicconfig.VisibilityArchivalQueryMaxPageSize),
		DisallowQuery:                               dc.GetBoolPropertyFilteredByDomain(dynamicconfig.DisallowQuery),
		SendRawWorkflowHistory:                      dc.GetBoolPropertyFilteredByDomain(dynamicconfig.SendRawWorkflowHistory),
		DecisionResultCountLimit:                    dc.GetIntPropertyFilteredByDomain(dynamicconfig.FrontendDecisionResultCountLimit),
		EmitSignalNameMetricsTag:                    dc.GetBoolPropertyFilteredByDomain(dynamicconfig.FrontendEmitSignalNameMetricsTag),
		Lockdown:                                    dc.GetBoolPropertyFilteredByDomain(dynamicconfig.Lockdown),
		domainConfig: domain.Config{
			MaxBadBinaryCount:      dc.GetIntPropertyFilteredByDomain(dynamicconfig.FrontendMaxBadBinaries),
			MinRetentionDays:       dc.GetIntProperty(dynamicconfig.MinRetentionDays),
			MaxRetentionDays:       dc.GetIntProperty(dynamicconfig.MaxRetentionDays),
			FailoverCoolDown:       dc.GetDurationPropertyFilteredByDomain(dynamicconfig.FrontendFailoverCoolDown),
			RequiredDomainDataKeys: dc.GetMapProperty(dynamicconfig.RequiredDomainDataKeys),
		},
	}
}

// TODO remove this and return 10 always, after cadence-web improve the List requests with backoff retry
// https://github.com/uber/cadence-web/issues/337
func defaultVisibilityListMaxQPS() int {
	cmd := strings.Join(os.Args, " ")
	// NOTE: this is safe because only dev box should start cadence in a single box with 4 services, and only docker should use `--env docker`
	if strings.Contains(cmd, "--root /etc/cadence --env docker start --services=history,matching,frontend,worker") {
		return 10000
	}
	return 10
}

// Service represents the cadence-frontend service
type Service struct {
	resource.Resource

	status       int32
	handler      *WorkflowHandler
	adminHandler AdminHandler
	stopC        chan struct{}
	config       *Config
	params       *resource.Params
}

// NewService builds a new cadence-frontend service
func NewService(
	params *resource.Params,
) (resource.Resource, error) {

	isAdvancedVisExistInConfig := len(params.PersistenceConfig.AdvancedVisibilityStore) != 0
	serviceConfig := NewConfig(
		dynamicconfig.NewCollection(
			params.DynamicConfig,
			params.Logger,
			dynamicconfig.ClusterNameFilter(params.ClusterMetadata.GetCurrentClusterName()),
		),
		params.PersistenceConfig.NumHistoryShards,
		isAdvancedVisExistInConfig,
	)
	params.PersistenceConfig.HistoryMaxConns = serviceConfig.HistoryMgrNumConns()

	serviceResource, err := resource.New(
		params,
		service.Frontend,
		&service.Config{
			PersistenceMaxQPS:       serviceConfig.PersistenceMaxQPS,
			PersistenceGlobalMaxQPS: serviceConfig.PersistenceGlobalMaxQPS,
			ThrottledLoggerMaxRPS:   serviceConfig.ThrottledLogRPS,

			EnableReadVisibilityFromES:    serviceConfig.EnableReadVisibilityFromES,
			AdvancedVisibilityWritingMode: nil, // frontend service never write

			EnableDBVisibilitySampling:                  serviceConfig.EnableVisibilitySampling,
			EnableReadDBVisibilityFromClosedExecutionV2: serviceConfig.EnableReadFromClosedExecutionV2,
			DBVisibilityListMaxQPS:                      serviceConfig.VisibilityListMaxQPS,
			WriteDBVisibilityOpenMaxQPS:                 nil, // frontend service never write
			WriteDBVisibilityClosedMaxQPS:               nil, // frontend service never write

			ESVisibilityListMaxQPS: serviceConfig.ESVisibilityListMaxQPS,
			ESIndexMaxResultWindow: serviceConfig.ESIndexMaxResultWindow,
			ValidSearchAttributes:  serviceConfig.ValidSearchAttributes,
		},
	)
	if err != nil {
		return nil, err
	}

	return &Service{
		Resource: serviceResource,
		status:   common.DaemonStatusInitialized,
		config:   serviceConfig,
		stopC:    make(chan struct{}),
		params:   params,
	}, nil
}

// Start starts the service
func (s *Service) Start() {
	if !atomic.CompareAndSwapInt32(&s.status, common.DaemonStatusInitialized, common.DaemonStatusStarted) {
		return
	}

	logger := s.GetLogger()
	logger.Info("frontend starting")

	// Base handler
	s.handler = NewWorkflowHandler(s, s.config, s.GetDomainReplicationQueue(), client.NewVersionChecker())

	// Additional decorations
	var handler Handler = s.handler
	if s.params.ClusterRedirectionPolicy != nil {
		handler = NewClusterRedirectionHandler(handler, s, s.config, *s.params.ClusterRedirectionPolicy)
	}

	handler = NewAccessControlledHandlerImpl(handler, s, s.params.Authorizer, s.params.AuthorizationConfig)

	// Register the latest (most decorated) handler
	thriftHandler := NewThriftHandler(handler)
	thriftHandler.register(s.GetDispatcher())

	grpcHandler := newGrpcHandler(handler)
	grpcHandler.register(s.GetDispatcher())

	s.adminHandler = NewAdminHandler(s, s.params, s.config)
	s.adminHandler = NewAccessControlledAdminHandlerImpl(s.adminHandler, s, s.params.Authorizer, s.params.AuthorizationConfig)

	adminThriftHandler := NewAdminThriftHandler(s.adminHandler)
	adminThriftHandler.register(s.GetDispatcher())

	adminGRPCHandler := newAdminGRPCHandler(s.adminHandler)
	adminGRPCHandler.register(s.GetDispatcher())

	// must start resource first
	s.Resource.Start()
	s.handler.Start()
	s.adminHandler.Start()

	// base (service is not started in frontend or admin handler) in case of race condition in yarpc registration function

	logger.Info("frontend started")

	<-s.stopC
}

// Stop stops the service
func (s *Service) Stop() {
	if !atomic.CompareAndSwapInt32(&s.status, common.DaemonStatusStarted, common.DaemonStatusStopped) {
		return
	}

	// initiate graceful shutdown:
	// 1. Fail rpc health check, this will cause client side load balancer to stop forwarding requests to this node
	// 2. wait for failure detection time
	// 3. stop taking new requests by returning InternalServiceError
	// 4. Wait for a second
	// 5. Stop everything forcefully and return

	requestDrainTime := common.MinDuration(time.Second, s.config.ShutdownDrainDuration())
	failureDetectionTime := common.MaxDuration(0, s.config.ShutdownDrainDuration()-requestDrainTime)

	s.GetLogger().Info("ShutdownHandler: Updating rpc health status to ShuttingDown")
	s.handler.UpdateHealthStatus(HealthStatusShuttingDown)

	s.GetLogger().Info("ShutdownHandler: Waiting for others to discover I am unhealthy")
	time.Sleep(failureDetectionTime)

	s.handler.Stop()
	s.adminHandler.Stop()

	s.GetLogger().Info("ShutdownHandler: Draining traffic")
	time.Sleep(requestDrainTime)

	close(s.stopC)
	s.Resource.Stop()
	s.params.Logger.Info("frontend stopped")
}
