package render

type ISVC struct {
	APIVersion string       `yaml:"apiVersion"`
	Kind       string       `yaml:"kind"`
	Metadata   ISVCMetadata `yaml:"metadata"`
	Spec       ISVCSpec     `yaml:"spec"`
}

type ISVCMetadata struct {
	Name        string            `yaml:"name"`
	Namespace   string            `yaml:"namespace"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}

type ISVCSpec struct {
	Predictor PredictorSpec `yaml:"predictor"`
}

type PredictorSpec struct {
	Model              ModelSpec `yaml:"model"`
	ServiceAccountName string    `yaml:"serviceAccountName,omitempty"`
}

type ModelSpec struct {
	ModelFormat ModelFormat `yaml:"modelFormat"`
	Runtime     string      `yaml:"runtime"`
	StorageUri  string      `yaml:"storageUri"`
	Env         []EnvVar    `yaml:"env,omitempty"`
	Resources   *Resources  `yaml:"resources"`
}

type ModelFormat struct {
	Name string `yaml:"name"`
}

type EnvVar struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type Resources struct {
	Limits   map[string]string `yaml:"limits"`
	Requests map[string]string `yaml:"requests"`
}
