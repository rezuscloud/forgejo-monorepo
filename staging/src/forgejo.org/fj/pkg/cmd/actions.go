package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	forgejo "forgejo.org/client-go"
	"github.com/spf13/cobra"
)

func newActionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "actions",
		Short: "Manage repository actions",
	}
	cmd.AddCommand(newActionsJobCmd())
	cmd.AddCommand(newActionsJobsCmd())
	cmd.AddCommand(newActionsLogsCmd())
	cmd.AddCommand(newActionsTasksCmd())
	cmd.AddCommand(newActionsDispatchCmd())
	cmd.AddCommand(newActionsWorkflowsCmd())
	cmd.AddCommand(newActionsRunsCmd())
	cmd.AddCommand(newActionsRerunCmd())
	cmd.AddCommand(newActionsVariablesCmd())
	cmd.AddCommand(newActionsSecretsCmd())
	return cmd
}

// resolveRunRef maps a user-supplied run reference to the run's database id.
// By default the reference is the run's index_in_repo — the number the web
// UI shows and `fj actions runs` prints — resolved through the run-by-index
// endpoint. rawID passes the reference through as a database id instead (#108).
func resolveRunRef(c *forgejo.Client, owner, repo, ref string, rawID bool) (int64, error) {
	n, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid run reference %q: %w", ref, err)
	}
	if rawID {
		return n, nil
	}
	run, _, err := c.Repo.ActionRunByIndex(context.Background(), owner, repo, n)
	if err != nil {
		return 0, fmt.Errorf("run index %d: %w", n, err)
	}
	return run.Id, nil
}

func newActionsJobsCmd() *cobra.Command {
	var rawID bool
	cmd := &cobra.Command{
		Use:   "jobs <RUN>",
		Short: "List the jobs in an action run",
		Long:  "RUN is the run's index — the number the web UI shows and 'fj actions runs'\nprints (index_in_repo). Pass --run-id to use the run's raw database id.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, owner, repo, err := resolveClient(cmd)
			if err != nil {
				return err
			}
			runID, err := resolveRunRef(c, owner, repo, args[0], rawID)
			if err != nil {
				return err
			}
			jobs, _, err := c.Repo.ListActionRunJobs(context.Background(), owner, repo, runID)
			if err != nil {
				return err
			}
			if len(jobs) == 0 {
				fmt.Println("no jobs")
				return nil
			}
			for _, j := range jobs {
				runsOn := strings.Join(j.RunsOn, ",")
				fmt.Printf("#%d %s [%s] runs_on:%s\n", j.Id, j.Name, j.Status, runsOn)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&rawID, "run-id", false, "treat RUN as the run's raw database id instead of its index")
	return cmd
}

func newActionsJobCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "job <ID>",
		Short: "View a single action job, including its steps",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jobID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid job id %q: %w", args[0], err)
			}
			c, owner, repo, err := resolveClient(cmd)
			if err != nil {
				return err
			}
			job, _, err := c.Repo.RepoGetActionJob(context.Background(), owner, repo, jobID)
			if err != nil {
				return err
			}
			fmt.Printf("#%d %s [%s] run:#%d attempt:%d\n", job.Id, job.Name, job.Status, job.RunId, job.Attempt)
			for _, s := range job.Steps {
				fmt.Printf("  %d [%s] %s\n", s.Number, s.Status, s.Name)
			}
			return nil
		},
	}
}

func newActionsLogsCmd() *cobra.Command {
	var jobID int64
	var runRef string
	var rawID bool
	var attempt int64
	var step int
	var outFile string
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "View the logs of an action run or job",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, owner, repo, err := resolveClient(cmd)
			if err != nil {
				return err
			}
			if jobID != 0 {
				logs, _, err := c.Repo.RepoGetActionJobLogs(context.Background(), owner, repo, jobID, attempt, step)
				if err != nil {
					return err
				}
				fmt.Print(logs)
				return nil
			}
			if runRef != "" {
				runID, err := resolveRunRef(c, owner, repo, runRef, rawID)
				if err != nil {
					return err
				}
				logs, _, err := c.Repo.RepoGetActionRunLogs(context.Background(), owner, repo, runID)
				if err != nil {
					return err
				}
				path := outFile
				if path == "" {
					path = fmt.Sprintf("run-%d-logs.zip", runID)
				}
				if err := os.WriteFile(path, []byte(logs), 0644); err != nil {
					return err
				}
				fmt.Printf("wrote %d bytes to %s\n", len(logs), path)
				return nil
			}
			return fmt.Errorf("must specify --job <ID> or --run <RUN>")
		},
	}
	cmd.Flags().Int64Var(&jobID, "job", 0, "print a single job's logs (plain text)")
	cmd.Flags().StringVar(&runRef, "run", "", "download all jobs' logs for a run (zip); RUN is the run's index unless --run-id is set")
	cmd.Flags().BoolVar(&rawID, "run-id", false, "treat --run's value as the run's raw database id instead of its index")
	cmd.Flags().Int64Var(&attempt, "attempt", 0, "with --job: fetch a specific historical attempt (default: latest)")
	cmd.Flags().IntVar(&step, "step", 0, "with --job: narrow to one step (number from `fj actions job`; omit for all steps)")
	cmd.Flags().StringVar(&outFile, "out", "", "output file for --run (default: run-<id>-logs.zip)")
	return cmd
}

func newActionsTasksCmd() *cobra.Command {
	var page int
	cmd := &cobra.Command{
		Use:   "tasks",
		Short: "List the action tasks on a repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, owner, repo, err := resolveClient(cmd)
			if err != nil {
				return err
			}
			res, _, err := c.Repo.ListActionTasks(context.Background(), owner, repo, page, 20, nil)
			if err != nil {
				return err
			}
			count := res.TotalCount
			if count == 1 {
				fmt.Println("1 task")
			} else {
				fmt.Printf("%d tasks\n", count)
			}
			for _, t := range res.WorkflowRuns {
				sym := statusSymbol(t.Status)
				sha := ""
				if t.HeadSha != "" && len(t.HeadSha) > 10 {
					sha = t.HeadSha[:10]
				}
				fmt.Printf("#%d (%s) %s %s (%s): %s\n",
					t.RunNumber, sha, sym, t.Name, t.Event, t.DisplayTitle)
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&page, "page", "p", 1, "page number")
	return cmd
}

func newActionsDispatchCmd() *cobra.Command {
	var inputs []string
	cmd := &cobra.Command{
		Use:   "dispatch <WORKFLOW> <REF>",
		Short: "Dispatch a workflow",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowName := args[0]
			ref := args[1]
			c, owner, repo, err := resolveClient(cmd)
			if err != nil {
				return err
			}
			inputMap := map[string]string{}
			for _, kv := range inputs {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid input %q (expected key=value)", kv)
				}
				inputMap[parts[0]] = parts[1]
			}
			body := &forgejo.DispatchWorkflowOption{
				Inputs:        inputMap,
				Ref:           ref,
				ReturnRunInfo: false,
			}
			_, _, err = c.Repo.DispatchWorkflow(context.Background(), owner, repo, workflowName, body)
			if err != nil {
				return err
			}
			fmt.Printf("Dispatched %s on %s with %d inputs\n", workflowName, ref, len(inputMap))
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&inputs, "input", "I", nil, "workflow input (key=value, repeatable)")
	return cmd
}

// #114: the dispatch API keys on the workflow FILENAME, which had no
// discovery surface — this lists it alongside the display name.
func newActionsWorkflowsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "workflows",
		Short: "List the repo's Action workflows (the filename is what dispatch takes)",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, owner, repo, err := resolveClient(cmd)
			if err != nil {
				return err
			}
			res, _, err := c.Repo.ListActionWorkflows(context.Background(), owner, repo)
			if err != nil {
				return err
			}
			if res.TotalCount == 0 {
				fmt.Println("no workflows")
				return nil
			}
			for _, w := range res.Workflows {
				fmt.Printf("%s\t%s\t%s\n", w.Filename, w.Name, w.State)
			}
			return nil
		},
	}
}

func newActionsRunsCmd() *cobra.Command {
	var page, limit int
	var status, event, headSha, ref, workflowID string
	var runNumber int64
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "List action runs on a repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, owner, repo, err := resolveClient(cmd)
			if err != nil {
				return err
			}
			res, _, err := c.Repo.ListActionRuns(context.Background(), owner, repo,
				page, limit, parseStringSlice(event), parseStringSlice(status), runNumber, headSha, ref, workflowID)
			if err != nil {
				return err
			}
			runs := res.WorkflowRuns
			if len(runs) == 0 {
				fmt.Println("no runs")
				return nil
			}
			for _, r := range runs {
				sym := statusSymbol(r.Status)
				sha := ""
				if r.CommitSha != "" && len(r.CommitSha) > 10 {
					sha = r.CommitSha[:10]
				}
				fmt.Printf("#%d (%s) %s (%s): %s\n",
					r.IndexInRepo, sha, sym, r.Event, r.Title)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&page, "page", 1, "page number")
	cmd.Flags().IntVar(&limit, "limit", 20, "runs per page")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (comma-separated: success,failure,running,...)")
	cmd.Flags().StringVar(&event, "event", "", "filter by event (comma-separated: push,pull_request,...)")
	cmd.Flags().StringVar(&headSha, "head-sha", "", "filter by head commit sha")
	cmd.Flags().StringVar(&ref, "ref", "", "filter by ref (branch)")
	cmd.Flags().StringVar(&workflowID, "workflow-id", "", "filter by workflow id")
	cmd.Flags().Int64Var(&runNumber, "run-number", 0, "filter by run number")
	return cmd
}

// newActionsRerunCmd wraps the rerun API endpoints (#153): all jobs of a
// run, only its failed jobs, or a single job (and its dependents).
func newActionsRerunCmd() *cobra.Command {
	var (
		rawID      bool
		failedOnly bool
		jobID      int64
	)
	cmd := &cobra.Command{
		Use:   "rerun <RUN>",
		Short: "Re-run a completed workflow run",
		Long: `RUN is the run's index — the number the web UI shows and 'fj actions runs'
prints (index_in_repo). Pass --run-id to use the run's raw database id.

By default every job of the run is re-run as a new attempt. --failed-only
re-runs only the jobs that failed. --job <ID> re-runs one job (and its
dependents) instead of the whole run; RUN is still required to select the
run unless --job addresses it directly via its database id.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, owner, repo, err := resolveClient(cmd)
			if err != nil {
				return err
			}
			if jobID != 0 {
				if failedOnly {
					return fmt.Errorf("--failed-only and --job are mutually exclusive")
				}
				if _, err := c.Repo.RerunActionJob(context.Background(), owner, repo, jobID); err != nil {
					return err
				}
				fmt.Printf("rerun queued for job %d\n", jobID)
				return nil
			}
			runID, err := resolveRunRef(c, owner, repo, args[0], rawID)
			if err != nil {
				return err
			}
			if failedOnly {
				if _, err := c.Repo.RerunFailedActionRun(context.Background(), owner, repo, runID); err != nil {
					return err
				}
				fmt.Printf("rerun queued for failed jobs of run %d\n", runID)
				return nil
			}
			if _, err := c.Repo.RerunActionRun(context.Background(), owner, repo, runID); err != nil {
				return err
			}
			fmt.Printf("rerun queued for run %d\n", runID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&rawID, "run-id", false, "treat RUN as the run's raw database id instead of its index")
	cmd.Flags().BoolVar(&failedOnly, "failed-only", false, "re-run only the failed jobs of the run")
	cmd.Flags().Int64Var(&jobID, "job", 0, "re-run a single job (and its dependents) by job id instead of the whole run")
	return cmd
}

func newActionsVariablesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "variables",
		Short: "Manage action variables",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List variables",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, owner, repo, err := resolveClient(cmd)
			if err != nil {
				return err
			}
			vars, _, err := c.Repo.GetRepoVariablesList(context.Background(), owner, repo, 1, 50)
			if err != nil {
				return err
			}
			for _, v := range vars {
				fmt.Printf("%s", v.Name)
				if v.Data != "" {
					fmt.Printf(" = %s", v.Data)
				}
				fmt.Println()
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "create <NAME> <VALUE>",
		Short: "Create a new variable",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, owner, repo, err := resolveClient(cmd)
			if err != nil {
				return err
			}
			_, err = c.Repo.CreateRepoVariable(context.Background(), owner, repo, args[0], &forgejo.CreateVariableOption{Value: args[1]})
			return err
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "delete <NAME>",
		Short: "Delete a variable",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, owner, repo, err := resolveClient(cmd)
			if err != nil {
				return err
			}
			_, err = c.Repo.DeleteRepoVariable(context.Background(), owner, repo, args[0])
			return err
		},
	})
	return cmd
}

func newActionsSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage action secrets",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List secrets",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, owner, repo, err := resolveClient(cmd)
			if err != nil {
				return err
			}
			secrets, _, err := c.Repo.RepoListActionsSecrets(context.Background(), owner, repo, 1, 50)
			if err != nil {
				return err
			}
			for _, s := range secrets {
				fmt.Println(s.Name)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "create <NAME> <VALUE>",
		Short: "Create or update a secret",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, owner, repo, err := resolveClient(cmd)
			if err != nil {
				return err
			}
			_, err = c.Repo.UpdateRepoSecret(context.Background(), owner, repo, args[0], &forgejo.CreateOrUpdateSecretOption{Data: args[1]})
			return err
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "delete <NAME>",
		Short: "Delete a secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, owner, repo, err := resolveClient(cmd)
			if err != nil {
				return err
			}
			_, err = c.Repo.DeleteRepoSecret(context.Background(), owner, repo, args[0])
			return err
		},
	})
	return cmd
}
