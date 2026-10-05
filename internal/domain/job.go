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
	Target  string
	Op      JobOp
	N       int
	Version int64
}

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

// Args encodes the job; ParseJob reverses it.
func (j Job) Args() []string {
	return []string{j.Target, string(j.Op), strconv.Itoa(j.N), strconv.FormatInt(j.Version, 10)}
}

// ParseJob decodes the output of Job.Args.
func ParseJob(args []string) (Job, error) {
	if len(args) != 4 {
		return Job{}, fmt.Errorf("a job needs 4 arguments, got %d", len(args))
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
	switch j.Op {
	case OpUp, OpDown, OpBatch, OpUpTo, OpDownTo, OpOnly, OpRedo:
		return j, nil
	}
	return Job{}, fmt.Errorf("unknown job operation %q", args[1])
}
