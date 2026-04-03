package driver

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"github.com/gluesys/flexa-csi/pkg/flexa/common"
)

const (
	clientInfoSecretName = "client-info-secret"
	clientInfoFileKey    = "client-info.yml"
)

func readPodNamespace() (string, error) {
	b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", err
	}
	ns := strings.TrimSpace(string(b))
	if ns == "" {
		return "", fmt.Errorf("pod namespace is empty")
	}
	return ns, nil
}

func (d *Driver) StartClientInfoSecretWatch() {
	if d == nil || d.K8sClient == nil {
		return
	}

	ns, err := readPodNamespace()
	if err != nil {
		log.Warnf("ClientInfo watch disabled: failed to read pod namespace: %v", err)
		return
	}

	tweak := func(opts *metav1.ListOptions) {
		opts.FieldSelector = fields.OneTermEqualSelector("metadata.name", clientInfoSecretName).String()
	}

	factory := informers.NewSharedInformerFactoryWithOptions(
		d.K8sClient,
		60*time.Minute,
		informers.WithNamespace(ns),
		informers.WithTweakListOptions(tweak),
	)
	inf := factory.Core().V1().Secrets().Informer()

	apply := func(sec *v1.Secret) {
		if sec == nil || sec.Name != clientInfoSecretName {
			return
		}
		raw, ok := sec.Data[clientInfoFileKey]
		if !ok || len(raw) == 0 {
			log.Warnf("ClientInfo secret %s/%s has no key %q", ns, clientInfoSecretName, clientInfoFileKey)
			return
		}
		cfg, err := common.LoadClientInfoConfigFromReader(strings.NewReader(string(raw)))
		if err != nil {
			log.Errorf("ClientInfo secret parse failed (keep previous): %v", err)
			return
		}
		d.SetClientInfoConfig(cfg)
		log.Infof("ClientInfo secret applied: default=%v profiles=%d", cfg.Default != nil, len(cfg.Profiles))
	}

	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if sec, ok := obj.(*v1.Secret); ok {
				apply(sec)
			}
		},
		UpdateFunc: func(_, newObj interface{}) {
			if sec, ok := newObj.(*v1.Secret); ok {
				apply(sec)
			}
		},
	})

	stopCh := make(chan struct{})
	factory.Start(stopCh)
	go func() {
		if !cache.WaitForCacheSync(stopCh, inf.HasSynced) {
			log.Warn("ClientInfo secret watch: cache sync failed")
			return
		}
		// Try an initial get in case Add event was missed.
		sec, err := d.K8sClient.CoreV1().Secrets(ns).Get(context.Background(), clientInfoSecretName, metav1.GetOptions{})
		if err == nil {
			apply(sec)
		}
		log.Infof("ClientInfo secret watch started: %s/%s", ns, clientInfoSecretName)
	}()
}

