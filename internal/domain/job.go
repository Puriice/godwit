package domain

import (
	"fmt"
	"strconv"
)

// JobOp names one kind of run a Job can perform.
type JobOp string

const (
	OpUp     JobOp = "up"     // apply N pending (all when N <= 0)
	OpDown   JobOp = "down"   // revert the N newest applied
	OpBatch  JobOp = "batch"  // revert the latest batch
	OpUpTo   JobOp = "upto"   // apply pending up to Version
	OpDownTo JobOp = "downto" // revert everything newer than Version
	OpOnly   JobOp = "only"   // apply just Version
	OpRedo   JobOp = "redo"   // revert and re-apply Version
)

// Job is one run on one target. It can be encoded as arguments, so a run can
// be handed to a separate process that outlives the one that started it.
type Job struct {
	// Service is the workspace service whose migrations run; empty in a plain
	// project.
	Service string
	Target  string
	Op      JobOp
	N       int
	Version int64
}

// Ref names the target the job runs on, in its service.
func (j Job) Ref() TargetRef { return TargetRef{Service: j.Service, Target: j.Target} }

// Direction is the direction progress events of this job are reported in.
func (j Job) Direction() Direction {
	switch j.Op {
	case OpDown, OpBatch, OpDownTo:
		return Down
	case OpRedo:
		return Redo
	}
	return Up
}

// Args encodes the job; ParseJob reverses it. A job without a service is
// encoded as four arguments, as before services existed; the service follows
// as a fifth, so files written by older versions still parse.
func (j Job) Args() []string {
	args := []string{j.Target, string(j.Op), strconv.Itoa(j.N), strconv.FormatInt(j.Version, 10)}
	if j.Service != "" {
		args = append(args, j.Service)
	}
	return args
}

// ParseJob decodes the output of Job.Args.
func ParseJob(args []string) (Job, error) {
	if len(args) != 4 && len(args) != 5 {
		return Job{}, fmt.Errorf("a job needs 4 or 5 arguments, got %d", len(args))
	}
	n, err := strconv.Atoi(args[2])
	if err != nil {
		return Job{}, fmt.Errorf("invalid job count %q", args[2])
	}
	v, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil {
		return Job{}, fmt.Errorf("invalid job version %q", args[3])
	}
	j := Job{Target: args[0], Op: JobOp(args[1]), N: n, Version: v}
	if len(args) == 5 {
		if err := ValidateServiceName(args[4]); err != nil {
			return Job{}, err
		}
		j.Service = args[4]
	}
	switch j.Op {
	case OpUp, OpDown, OpBatch, OpUpTo, OpDownTo, OpOnly, OpRedo:
		return j, nil
	}
	return Job{}, fmt.Errorf("unknown job operation %q", args[1])
}
