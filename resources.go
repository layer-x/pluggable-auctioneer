package auctioneer

import (
	"errors"

	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/rep"
	"strings"
)

type TaskStartRequest struct {
	rep.Task
}

func NewTaskStartRequest(task rep.Task) TaskStartRequest {
	return TaskStartRequest{task}
}

func NewTaskStartRequestFromModel(t *models.Task) TaskStartRequest {
	var tags []string
	for _, envVar := range t.EnvironmentVariables {
		if envVar.Name == "DIEGO_BRAIN_TAG" {
			tags = strings.Split(envVar.Value, ",")
		}
	}
	return TaskStartRequest{rep.NewTask(t.TaskGuid, t.Domain, rep.NewResource(t.MemoryMb, t.DiskMb, t.RootFs), tags...)}
}

func (t *TaskStartRequest) Validate() error {
	switch {
	case t.TaskGuid == "":
		return errors.New("task guid is empty")
	case t.Resource.Empty():
		return errors.New("resources cannot be empty")
	default:
		return nil
	}
}

type LRPStartRequest struct {
	ProcessGuid string `json:"process_guid"`
	Domain      string `json:"domain"`
	Indices     []int  `json:"indices"`
	EnvironmentVariables []*models.EnvironmentVariable `json:"environment_variables"`
	rep.Resource
}

func NewLRPStartRequest(processGuid, domain string, indices []int, res rep.Resource, env []*models.EnvironmentVariable) LRPStartRequest {
	return LRPStartRequest{
		ProcessGuid: processGuid,
		Domain:      domain,
		Indices:     indices,
		Resource:    res,
		EnvironmentVariables: env,
	}
}

func NewLRPStartRequestFromModel(d *models.DesiredLRP, indices ...int) LRPStartRequest {
	return NewLRPStartRequest(d.ProcessGuid, d.Domain, indices, rep.NewResource(d.MemoryMb, d.DiskMb, d.RootFs), d.EnvironmentVariables)
}

func NewLRPStartRequestFromSchedulingInfo(s *models.DesiredLRPSchedulingInfo, indices ...int) LRPStartRequest {
	emptyEnvironmentVariables := []*models.EnvironmentVariable{}
	return NewLRPStartRequest(s.ProcessGuid, s.Domain, indices, rep.NewResource(s.MemoryMb, s.DiskMb, s.RootFs), emptyEnvironmentVariables)
}

func (lrpstart *LRPStartRequest) Validate() error {
	switch {
	case lrpstart.ProcessGuid == "":
		return errors.New("proccess guid is empty")
	case lrpstart.Domain == "":
		return errors.New("domain is empty")
	case len(lrpstart.Indices) == 0:
		return errors.New("indices must not be empty")
	case lrpstart.Resource.Empty():
		return errors.New("resources cannot be empty")
	default:
		return nil
	}
}
