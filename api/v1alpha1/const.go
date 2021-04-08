package v1alpha1

const (
	// conditions
	ConditionCompleted    = "Completed"    // the pipeline completed
	ConditionRunning      = "Running"      // added if any step is currently running
	ConditionSunkMessages = "SunkMessages" // added if any messages have been written to a sink for any step
	ConditionTerminating  = "Terminating"  // added if any terminator step terminated
	// container names
	CtrInit    = "init"
	CtrMain    = "main"
	CtrSidecar = "sidecar"
	// env vars
	EnvNamespace    = "ARGO_DATAFLOW_NAMESPACE"
	EnvPipelineName = "ARGO_DATAFLOW_PIPELINE_NAME"
	EnvReplica      = "ARGO_DATAFLOW_REPLICA"
	EnvStepSpec     = "ARGO_DATAFLOW_STEP_SPEC"
	// label/annotation keys
	KeyPipelineName = "dataflow.argoproj.io/pipeline-name"
	KeyReplica      = "dataflow.argoproj.io/replica"
	KeyStepName     = "dataflow.argoproj.io/step-name" // the step name without pipeline name prefix
	KeySpecHash     = "dataflow.argoproj.io/spec-hash" // hash of the spec
	// paths
	PathCheckout   = "/var/run/argo-dataflow/checkout"
	PathEmptyDirs  = "empty-dirs"
	PathFIFOIn     = "/var/run/argo-dataflow/in"
	PathFIFOOut    = "/var/run/argo-dataflow/out"
	PathGroups     = "/var/run/argo-dataflow/groups"
	PathRuntimes   = "runtimes"
	PathWorkingDir = "/var/run/argo-dataflow/wd"
	PathVarRun     = "/var/run/argo-dataflow"
)
