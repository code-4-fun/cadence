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
package xdc

import (
	"flag"
	"fmt"
	"github.com/pborman/uuid"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"github.com/uber/cadence/.gen/go/history"
	"github.com/uber/cadence/.gen/go/shared"
	"github.com/uber/cadence/common"
	"github.com/uber/cadence/common/cache"
	"github.com/uber/cadence/common/log"
	"github.com/uber/cadence/common/log/loggerimpl"
	"github.com/uber/cadence/common/log/tag"
	"github.com/uber/cadence/common/persistence"
	"github.com/uber/cadence/common/xdc"
	"github.com/uber/cadence/environment"
	"github.com/uber/cadence/host"
	"go.uber.org/zap"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"os"
	"strconv"
	"testing"
	"time"
)

type (
	nDCIntegrationTestSuite struct {
		// override suite.Suite.Assertions with require.Assertions; this means that s.NotNil(nil) will stop the test,
		// not merely log an error
		*require.Assertions
		suite.Suite
		cluster1  *host.TestCluster
		cluster2  *host.TestCluster
		logger    log.Logger
		generator xdc.Generator
	}
)

func TestNDCIntegrationTestSuite(t *testing.T) {
	flag.Parse()
	suite.Run(t, new(nDCIntegrationTestSuite))
}

func (s *nDCIntegrationTestSuite) SetupSuite() {
	zapLogger, err := zap.NewDevelopment()
	// cannot use s.Nil since it is not initialized
	s.Require().NoError(err)
	s.logger = loggerimpl.NewLogger(zapLogger)

	fileName := "../testdata/xdc_integration_test_clusters.yaml"
	if host.TestFlags.TestClusterConfigFile != "" {
		fileName = host.TestFlags.TestClusterConfigFile
	}
	environment.SetupEnv()

	confContent, err := ioutil.ReadFile(fileName)
	s.Require().NoError(err)
	confContent = []byte(os.ExpandEnv(string(confContent)))

	var clusterConfigs []*host.TestClusterConfig
	s.Require().NoError(yaml.Unmarshal(confContent, &clusterConfigs))

	c, err := host.NewCluster(clusterConfigs[0], s.logger.WithTags(tag.ClusterName(clusterName[0])))
	s.Require().NoError(err)
	s.cluster1 = c

	c, err = host.NewCluster(clusterConfigs[1], s.logger.WithTags(tag.ClusterName(clusterName[1])))
	s.Require().NoError(err)
	s.cluster2 = c
	s.generator = xdc.InitializaEventGenerator()
}

func (s *nDCIntegrationTestSuite) SetupTest() {
	// Have to define our overridden assertions in the test setup. If we did it earlier, s.T() will return nil
	s.Assertions = require.New(s.T())
	s.generator.Reset()
}

func (s *nDCIntegrationTestSuite) TearDownSuite() {
	s.cluster1.TearDownCluster()
	s.cluster2.TearDownCluster()
}

func (s *nDCIntegrationTestSuite) TestSimpleNDC() {
	domainName := "test-simple-workflow-ndc-" + common.GenerateRandomString(5)
	client1 := s.cluster1.GetFrontendClient() // active
	regReq := &shared.RegisterDomainRequest{
		Name:                                   common.StringPtr(domainName),
		IsGlobalDomain:                         common.BoolPtr(true),
		Clusters:                               clusterReplicationConfig,
		ActiveClusterName:                      common.StringPtr(clusterName[0]),
		WorkflowExecutionRetentionPeriodInDays: common.Int32Ptr(1),
	}
	err := client1.RegisterDomain(createContext(), regReq)
	s.NoError(err)

	descReq := &shared.DescribeDomainRequest{
		Name: common.StringPtr(domainName),
	}
	resp, err := client1.DescribeDomain(createContext(), descReq)
	s.NoError(err)
	s.NotNil(resp)
	// Wait for domain cache to pick the change
	time.Sleep(cache.DomainCacheRefreshInterval)
	root := &xdc.NDCTestBranch{
		Batches: make([]xdc.NDCTestBatch, 0),
	}
	for s.generator.HasNextVertex() {
		events := s.generator.GetNextVertices()
		newBatch := xdc.NDCTestBatch{
			Events: events,
		}
		root.Batches = append(root.Batches, newBatch)
	}

	identity := "test-event-generator"
	wid := uuid.New()
	rid := uuid.New()
	wt := "event-generator-workflow-type"
	tl := "event-generator-taskList"
	domain := *resp.DomainInfo.Name
	domainID := *resp.DomainInfo.UUID
	version := int64(100)
	attributeGenerator := xdc.NewHistoryAttributesGenerator(wid, rid, tl, wt, domainID, domain, identity)
	historyBatch := attributeGenerator.GenerateHistoryEvents(root.Batches, 1, version)

	historyClient := s.cluster2.GetHistoryClient()
	replicationInfo := make(map[string]*shared.ReplicationInfo)
	replicationInfo["active"] = &shared.ReplicationInfo{
		Version:     common.Int64Ptr(version),
		LastEventId: common.Int64Ptr(0),
	}

	replicationInfo["standby"] = &shared.ReplicationInfo{
		Version:     common.Int64Ptr(version),
		LastEventId: common.Int64Ptr(0),
	}

	for idx, batch := range historyBatch {
		fmt.Println("#######:" + strconv.Itoa(idx))
		fmt.Println(*batch)
		fmt.Println("####################")
		var newRunHistory *shared.History = nil
		// Generate a new run history on continue as new
		if idx == len(historyBatch)-1 && batch.Events[len(batch.Events)-1].GetWorkflowExecutionContinuedAsNewEventAttributes() != nil {
			newRunID := uuid.New()
			batch.Events[len(batch.Events)-1].WorkflowExecutionContinuedAsNewEventAttributes.NewExecutionRunId = common.StringPtr(newRunID)
			newRunHistory = &shared.History{
				Events: []*shared.HistoryEvent{
					{
						EventId:   common.Int64Ptr(1),
						Timestamp: common.Int64Ptr(time.Now().UnixNano()),
						EventType: common.EventTypePtr(shared.EventTypeWorkflowExecutionStarted),
						Version:   common.Int64Ptr(version),
						TaskId:    common.Int64Ptr(1),
						WorkflowExecutionStartedEventAttributes: &shared.WorkflowExecutionStartedEventAttributes{
							WorkflowType:         common.WorkflowTypePtr(shared.WorkflowType{Name:common.StringPtr(wt)}),
							ParentWorkflowDomain: common.StringPtr(domain),
							ParentWorkflowExecution: &shared.WorkflowExecution{
								WorkflowId: common.StringPtr(wid),
								RunId:      common.StringPtr(rid),
							},
							ParentInitiatedEventId: common.Int64Ptr(batch.Events[len(batch.Events)-1].GetEventId()),
							TaskList: common.TaskListPtr(shared.TaskList{
								Name: common.StringPtr(tl),
								Kind: common.TaskListKindPtr(shared.TaskListKindNormal),
							}),
							ExecutionStartToCloseTimeoutSeconds: common.Int32Ptr(10),
							TaskStartToCloseTimeoutSeconds:      common.Int32Ptr(10),
							ContinuedExecutionRunId:             common.StringPtr(rid),
							Initiator:                           shared.ContinueAsNewInitiatorCronSchedule.Ptr(),
							OriginalExecutionRunId:              common.StringPtr(rid),
							Identity:                            common.StringPtr("NDC-test"),
							FirstExecutionRunId:                 common.StringPtr(rid),
							Attempt:                             common.Int32Ptr(0),
							ExpirationTimestamp:                 common.Int64Ptr(time.Now().Add(time.Minute).UnixNano()),
						},
					},
				},
			}
		}
		err = historyClient.ReplicateEvents(createContext(), &history.ReplicateEventsRequest{
			SourceCluster: common.StringPtr("active"),
			DomainUUID:    resp.DomainInfo.UUID,
			WorkflowExecution: &shared.WorkflowExecution{
				WorkflowId: common.StringPtr(wid),
				RunId:      common.StringPtr(rid),
			},
			FirstEventId:            batch.Events[0].EventId,
			NextEventId:             common.Int64Ptr(*batch.Events[len(batch.Events)-1].EventId + int64(1)),
			Version:                 common.Int64Ptr(version),
			History:                 batch,
			NewRunHistory:           newRunHistory,
			ForceBufferEvents:       common.BoolPtr(false),
			EventStoreVersion:       common.Int32Ptr(persistence.EventStoreVersionV2),
			NewRunEventStoreVersion: common.Int32Ptr(persistence.EventStoreVersionV2),
			ResetWorkflow:           common.BoolPtr(false),
		})
		s.Nil(err)
	}

	passiveClient := s.cluster2.GetFrontendClient()
	replicatedHistory, err := passiveClient.GetWorkflowExecutionHistory(createContext(), &shared.GetWorkflowExecutionHistoryRequest{
		Domain: common.StringPtr(domain),
		Execution: &shared.WorkflowExecution{
			WorkflowId: common.StringPtr(wid),
			RunId:      common.StringPtr(rid),
		},
		MaximumPageSize:        common.Int32Ptr(10000),
		NextPageToken:          nil,
		WaitForNewEvent:        common.BoolPtr(false),
		HistoryEventFilterType: shared.HistoryEventFilterTypeAllEvent.Ptr(),
	})

	s.Nil(err)
	batchIndex := 0
	batch := historyBatch[batchIndex].GetEvents()
	eventIndex := 0
	for _, event := range replicatedHistory.GetHistory().GetEvents() {
		if eventIndex >= len(batch) {
			batchIndex++
			batch = historyBatch[batchIndex].GetEvents()
			eventIndex = 0
		}
		originEvent := batch[eventIndex]
		eventIndex++
		s.Equal(originEvent.GetEventType().String(), event.GetEventType().String())
	}
}
