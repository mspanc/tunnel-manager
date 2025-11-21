package client

import (
	"fmt"
	"tunnel/internal/config"

	"github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type Client struct {
	KubeClient       *kubernetes.Clientset
	CloudFlareClient *cloudflare.Client
}

func NewClient(config *config.Config) (*Client, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubernetes in-cluster config: %v", err)
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %v", err)
	}

	cfClient := cloudflare.NewClient(option.WithAPIToken(config.CloudFlareAPIToken))

	return &Client{
		KubeClient:       kubeClient,
		CloudFlareClient: cfClient,
	}, nil
}
