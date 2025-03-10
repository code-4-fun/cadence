// Copyright (c) 2017 Uber Technologies, Inc.
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

package persistence

import (
	"context"
	"runtime"
	"sync"

	"github.com/uber/cadence/common/clock"
	"github.com/uber/cadence/common/dynamicconfig"
	"github.com/uber/cadence/common/log"
	"github.com/uber/cadence/common/log/tag"
	"github.com/uber/cadence/common/metrics"
	"github.com/uber/cadence/common/tokenbucket"
	"github.com/uber/cadence/common/types"
)

const (
	// To sample visibility request, open has only 1 bucket, closed has 2
	numOfPriorityForOpen   = 1
	numOfPriorityForClosed = 2
	numOfPriorityForList   = 1
)

// errPersistenceLimitExceededForList is the error indicating QPS limit reached for list visibility.
var errPersistenceLimitExceededForList = &types.ServiceBusyError{Message: "Persistence Max QPS Reached for List Operations."}

type visibilitySamplingClient struct {
	rateLimitersForOpen   *domainToBucketMap
	rateLimitersForClosed *domainToBucketMap
	rateLimitersForList   *domainToBucketMap
	persistence           VisibilityManager
	config                *SamplingConfig
	metricClient          metrics.Client
	logger                log.Logger
}

var _ VisibilityManager = (*visibilitySamplingClient)(nil)

type (
	// SamplingConfig is config for visibility
	SamplingConfig struct {
		VisibilityOpenMaxQPS dynamicconfig.IntPropertyFnWithDomainFilter `yaml:"-" json:"-"`
		// VisibilityClosedMaxQPS max QPS for record closed workflows
		VisibilityClosedMaxQPS dynamicconfig.IntPropertyFnWithDomainFilter `yaml:"-" json:"-"`
		// VisibilityListMaxQPS max QPS for list workflow
		VisibilityListMaxQPS dynamicconfig.IntPropertyFnWithDomainFilter `yaml:"-" json:"-"`
	}
)

// NewVisibilitySamplingClient creates a client to manage visibility with sampling
// For write requests, it will do sampling which will lose some records
// For read requests, it will do sampling which will return service busy errors.
// Note that this is different from NewVisibilityPersistenceRateLimitedClient which is overlapping with the read processing.
func NewVisibilitySamplingClient(persistence VisibilityManager, config *SamplingConfig, metricClient metrics.Client, logger log.Logger) VisibilityManager {
	return &visibilitySamplingClient{
		persistence:           persistence,
		rateLimitersForOpen:   newDomainToBucketMap(),
		rateLimitersForClosed: newDomainToBucketMap(),
		rateLimitersForList:   newDomainToBucketMap(),
		config:                config,
		metricClient:          metricClient,
		logger:                logger,
	}
}

type domainToBucketMap struct {
	sync.RWMutex
	mappings map[string]tokenbucket.PriorityTokenBucket
}

func newDomainToBucketMap() *domainToBucketMap {
	return &domainToBucketMap{
		mappings: make(map[string]tokenbucket.PriorityTokenBucket),
	}
}

func (m *domainToBucketMap) getRateLimiter(domain string, numOfPriority, qps int) tokenbucket.PriorityTokenBucket {
	m.RLock()
	rateLimiter, exist := m.mappings[domain]
	m.RUnlock()

	if exist {
		return rateLimiter
	}

	m.Lock()
	if rateLimiter, ok := m.mappings[domain]; ok { // read again to ensure no duplicate create
		m.Unlock()
		return rateLimiter
	}
	rateLimiter = tokenbucket.NewFullPriorityTokenBucket(numOfPriority, qps, clock.NewRealTimeSource())
	m.mappings[domain] = rateLimiter
	m.Unlock()
	return rateLimiter
}

func (p *visibilitySamplingClient) RecordWorkflowExecutionStarted(
	ctx context.Context,
	request *RecordWorkflowExecutionStartedRequest,
) error {
	domain := request.Domain
	domainID := request.DomainUUID

	rateLimiter := p.rateLimitersForOpen.getRateLimiter(domain, numOfPriorityForOpen, p.config.VisibilityOpenMaxQPS(domain))
	if ok, _ := rateLimiter.GetToken(0, 1); ok {
		return p.persistence.RecordWorkflowExecutionStarted(ctx, request)
	}

	p.logger.Info("Request for open workflow is sampled",
		tag.WorkflowDomainID(domainID),
		tag.WorkflowDomainName(domain),
		tag.WorkflowType(request.WorkflowTypeName),
		tag.WorkflowID(request.Execution.GetWorkflowID()),
		tag.WorkflowRunID(request.Execution.GetRunID()),
	)
	p.metricClient.IncCounter(metrics.PersistenceRecordWorkflowExecutionStartedScope, metrics.PersistenceSampledCounter)
	return nil
}

func (p *visibilitySamplingClient) RecordWorkflowExecutionClosed(
	ctx context.Context,
	request *RecordWorkflowExecutionClosedRequest,
) error {
	domain := request.Domain
	domainID := request.DomainUUID
	priority := getRequestPriority(request)

	rateLimiter := p.rateLimitersForClosed.getRateLimiter(domain, numOfPriorityForClosed, p.config.VisibilityClosedMaxQPS(domain))
	if ok, _ := rateLimiter.GetToken(priority, 1); ok {
		return p.persistence.RecordWorkflowExecutionClosed(ctx, request)
	}

	p.logger.Info("Request for closed workflow is sampled",
		tag.WorkflowDomainID(domainID),
		tag.WorkflowDomainName(domain),
		tag.WorkflowType(request.WorkflowTypeName),
		tag.WorkflowID(request.Execution.GetWorkflowID()),
		tag.WorkflowRunID(request.Execution.GetRunID()),
	)
	p.metricClient.IncCounter(metrics.PersistenceRecordWorkflowExecutionClosedScope, metrics.PersistenceSampledCounter)
	return nil
}

func (p *visibilitySamplingClient) RecordWorkflowExecutionUninitialized(
	ctx context.Context,
	request *RecordWorkflowExecutionUninitializedRequest,
) error {
	return p.persistence.RecordWorkflowExecutionUninitialized(ctx, request)
}

func (p *visibilitySamplingClient) UpsertWorkflowExecution(
	ctx context.Context,
	request *UpsertWorkflowExecutionRequest,
) error {
	domain := request.Domain
	domainID := request.DomainUUID

	rateLimiter := p.rateLimitersForClosed.getRateLimiter(domain, numOfPriorityForClosed, p.config.VisibilityClosedMaxQPS(domain))
	if ok, _ := rateLimiter.GetToken(0, 1); ok {
		return p.persistence.UpsertWorkflowExecution(ctx, request)
	}

	p.logger.Info("Request for upsert workflow is sampled",
		tag.WorkflowDomainID(domainID),
		tag.WorkflowDomainName(domain),
		tag.WorkflowType(request.WorkflowTypeName),
		tag.WorkflowID(request.Execution.GetWorkflowID()),
		tag.WorkflowRunID(request.Execution.GetRunID()),
	)
	p.metricClient.IncCounter(metrics.PersistenceUpsertWorkflowExecutionScope, metrics.PersistenceSampledCounter)
	return nil
}

func (p *visibilitySamplingClient) ListOpenWorkflowExecutions(
	ctx context.Context,
	request *ListWorkflowExecutionsRequest,
) (*ListWorkflowExecutionsResponse, error) {
	if err := p.tryConsumeListToken(request.Domain); err != nil {
		return nil, err
	}

	return p.persistence.ListOpenWorkflowExecutions(ctx, request)
}

func (p *visibilitySamplingClient) ListClosedWorkflowExecutions(
	ctx context.Context,
	request *ListWorkflowExecutionsRequest,
) (*ListWorkflowExecutionsResponse, error) {
	if err := p.tryConsumeListToken(request.Domain); err != nil {
		return nil, err
	}

	return p.persistence.ListClosedWorkflowExecutions(ctx, request)
}

func (p *visibilitySamplingClient) ListOpenWorkflowExecutionsByType(
	ctx context.Context,
	request *ListWorkflowExecutionsByTypeRequest,
) (*ListWorkflowExecutionsResponse, error) {
	if err := p.tryConsumeListToken(request.Domain); err != nil {
		return nil, err
	}

	return p.persistence.ListOpenWorkflowExecutionsByType(ctx, request)
}

func (p *visibilitySamplingClient) ListClosedWorkflowExecutionsByType(
	ctx context.Context,
	request *ListWorkflowExecutionsByTypeRequest,
) (*ListWorkflowExecutionsResponse, error) {
	if err := p.tryConsumeListToken(request.Domain); err != nil {
		return nil, err
	}

	return p.persistence.ListClosedWorkflowExecutionsByType(ctx, request)
}

func (p *visibilitySamplingClient) ListOpenWorkflowExecutionsByWorkflowID(
	ctx context.Context,
	request *ListWorkflowExecutionsByWorkflowIDRequest,
) (*ListWorkflowExecutionsResponse, error) {
	if err := p.tryConsumeListToken(request.Domain); err != nil {
		return nil, err
	}

	return p.persistence.ListOpenWorkflowExecutionsByWorkflowID(ctx, request)
}

func (p *visibilitySamplingClient) ListClosedWorkflowExecutionsByWorkflowID(
	ctx context.Context,
	request *ListWorkflowExecutionsByWorkflowIDRequest,
) (*ListWorkflowExecutionsResponse, error) {
	if err := p.tryConsumeListToken(request.Domain); err != nil {
		return nil, err
	}

	return p.persistence.ListClosedWorkflowExecutionsByWorkflowID(ctx, request)
}

func (p *visibilitySamplingClient) ListClosedWorkflowExecutionsByStatus(
	ctx context.Context,
	request *ListClosedWorkflowExecutionsByStatusRequest,
) (*ListWorkflowExecutionsResponse, error) {
	if err := p.tryConsumeListToken(request.Domain); err != nil {
		return nil, err
	}

	return p.persistence.ListClosedWorkflowExecutionsByStatus(ctx, request)
}

func (p *visibilitySamplingClient) GetClosedWorkflowExecution(
	ctx context.Context,
	request *GetClosedWorkflowExecutionRequest,
) (*GetClosedWorkflowExecutionResponse, error) {
	return p.persistence.GetClosedWorkflowExecution(ctx, request)
}

func (p *visibilitySamplingClient) DeleteWorkflowExecution(
	ctx context.Context,
	request *VisibilityDeleteWorkflowExecutionRequest,
) error {
	return p.persistence.DeleteWorkflowExecution(ctx, request)
}

func (p *visibilitySamplingClient) ListWorkflowExecutions(
	ctx context.Context,
	request *ListWorkflowExecutionsByQueryRequest,
) (*ListWorkflowExecutionsResponse, error) {
	return p.persistence.ListWorkflowExecutions(ctx, request)
}

func (p *visibilitySamplingClient) ScanWorkflowExecutions(
	ctx context.Context,
	request *ListWorkflowExecutionsByQueryRequest,
) (*ListWorkflowExecutionsResponse, error) {
	return p.persistence.ScanWorkflowExecutions(ctx, request)
}

func (p *visibilitySamplingClient) CountWorkflowExecutions(
	ctx context.Context,
	request *CountWorkflowExecutionsRequest,
) (*CountWorkflowExecutionsResponse, error) {
	return p.persistence.CountWorkflowExecutions(ctx, request)
}

func (p *visibilitySamplingClient) Close() {
	p.persistence.Close()
}

func (p *visibilitySamplingClient) GetName() string {
	return p.persistence.GetName()
}

func getRequestPriority(request *RecordWorkflowExecutionClosedRequest) int {
	priority := 0
	if request.Status == types.WorkflowExecutionCloseStatusCompleted {
		priority = 1 // low priority for completed workflows
	}
	return priority
}

func (p *visibilitySamplingClient) tryConsumeListToken(domain string) error {
	rateLimiter := p.rateLimitersForList.getRateLimiter(domain, numOfPriorityForList, p.config.VisibilityListMaxQPS(domain))
	ok, _ := rateLimiter.GetToken(0, 1)
	if ok {
		p.logger.Debug("List API request consumed QPS token", tag.WorkflowDomainName(domain), tag.Name(callerFuncName(2)))
		return nil
	}
	p.logger.Debug("List API request is being sampled", tag.WorkflowDomainName(domain), tag.Name(callerFuncName(2)))
	return errPersistenceLimitExceededForList
}

func callerFuncName(skip int) string {
	pc, _, _, ok := runtime.Caller(skip)
	details := runtime.FuncForPC(pc)
	if ok && details != nil {
		return details.Name()
	}
	return ""
}
