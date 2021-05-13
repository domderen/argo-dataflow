package sidecar

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Shopify/sarama"
	"github.com/nats-io/stan.go"
	"github.com/robfig/cron/v3"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	runtimeutil "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	apiutil "github.com/argoproj-labs/argo-dataflow/api/util"
	dfv1 "github.com/argoproj-labs/argo-dataflow/api/v1alpha1"
	"github.com/argoproj-labs/argo-dataflow/runner/util"
)

var (
	logger              = zap.New()
	debug               = logger.V(6)
	trace               = logger.V(8)
	closers             []func() error
	dynamicInterface    dynamic.Interface
	kubernetesInterface kubernetes.Interface
	updateInterval      time.Duration
	replica             = 0
	pipelineName        = os.Getenv(dfv1.EnvPipelineName)
	namespace           = os.Getenv(dfv1.EnvNamespace)
	spec                *dfv1.StepSpec
	sourceStatues       = dfv1.SourceStatuses{}
	sinkStatues         = dfv1.SinkStatuses{}
	mu                  = sync.RWMutex{}
)

func withLock(f func()) {
	mu.Lock()
	defer mu.Unlock()
	f()
}

func init() {
	sarama.Logger = util.NewSaramaStdLogger(logger)
}

func Exec(ctx context.Context) error {
	defer func() {
		for i := len(closers) - 1; i >= 0; i-- {
			if err := closers[i](); err != nil {
				logger.Error(err, "failed to close")
			}
		}
	}()
	defer patchStepStatus(context.Background()) // always patch a final status, we use a new context in case we've been SIGTERM

	restConfig := ctrl.GetConfigOrDie()
	dynamicInterface = dynamic.NewForConfigOrDie(restConfig)
	kubernetesInterface = kubernetes.NewForConfigOrDie(restConfig)

	if v, err := util.UnmarshallSpec(); err != nil {
		return err
	} else {
		spec = v
	}
	if v, err := strconv.Atoi(os.Getenv(dfv1.EnvReplica)); err != nil {
		return err
	} else {
		replica = v
	}
	if v, err := time.ParseDuration(os.Getenv(dfv1.EnvUpdateInterval)); err != nil {
		return err
	} else {
		updateInterval = v
	}

	if err := enrichSpec(ctx); err != nil {
		return err
	}

	logger.Info("sidecar config", "stepName", spec.Name, "pipelineName", pipelineName, "replica", replica, "updateInterval", updateInterval.String())

	toSink, err := connectSink()
	if err != nil {
		return err
	}

	connectOut(toSink)

	toMain, err := connectTo(ctx)
	if err != nil {
		return err
	}

	if err := connectSources(ctx, toMain); err != nil {
		return err
	}

	go func() {
		defer runtimeutil.HandleCrash(runtimeutil.PanicHandlers...)
		lastStatus := &dfv1.StepStatus{}
		for {
			status := &dfv1.StepStatus{
				SourceStatues: sourceStatues,
				SinkStatues:   sinkStatues,
			}
			if apiutil.NotEqual(lastStatus, status) {
				patchStepStatus(ctx)
				lastStatus = status.DeepCopy()
			}
			time.Sleep(updateInterval)
		}
	}()
	logger.Info("ready")
	<-ctx.Done()
	logger.Info("done")
	return nil
}

func patchStepStatus(ctx context.Context) {
	// we need to be careful to just patch fields we own
	patch := apiutil.MustJSON(map[string]interface{}{
		"status": map[string]interface{}{
			"sourceStatuses": sourceStatues,
			"sinkStatuses":   sinkStatues,
		},
	})
	logger.Info("patching step status (sinks/sources)", "patch", patch)
	if _, err := dynamicInterface.
		Resource(dfv1.StepGroupVersionResource).
		Namespace(namespace).
		Patch(
			ctx,
			pipelineName+"-"+spec.Name,
			types.MergePatchType,
			[]byte(patch),
			metav1.PatchOptions{},
			"status",
		); err != nil {
		logger.Error(err, "failed to patch step status")
	}
}

func enrichSpec(ctx context.Context) error {
	secrets := kubernetesInterface.CoreV1().Secrets(namespace)
	for i, source := range spec.Sources {
		if s := source.STAN; s != nil {
			secret, err := secrets.Get(ctx, "dataflow-stan-"+s.Name, metav1.GetOptions{})
			if err != nil {
				if !apierr.IsNotFound(err) {
					return err
				}
			} else {
				s.NATSURL = dfv1.StringOr(s.NATSURL, string(secret.Data["natsUrl"]))
				s.ClusterID = dfv1.StringOr(s.ClusterID, string(secret.Data["clusterId"]))
				s.SubjectPrefix = dfv1.SubjectPrefixOr(s.SubjectPrefix, dfv1.SubjectPrefix(secret.Data["subjectPrefix"]))
			}
			switch s.SubjectPrefix {
			case dfv1.SubjectPrefixNamespaceName:
				s.Subject = fmt.Sprintf("%s.%s", namespace, s.Subject)
			case dfv1.SubjectPrefixNamespacedPipelineName:
				s.Subject = fmt.Sprintf("%s.%s.%s", namespace, pipelineName, s.Subject)
			}
			source.STAN = s
		} else if k := source.Kafka; k != nil {
			secret, err := secrets.Get(ctx, "dataflow-kafka-"+k.Name, metav1.GetOptions{})
			if err != nil {
				if !apierr.IsNotFound(err) {
					return err
				}
			} else {
				k.Brokers = dfv1.StringsOr(k.Brokers, strings.Split(string(secret.Data["brokers"]), ","))
			}
			source.Kafka = k
		}
		spec.Sources[i] = source
	}

	for i, sink := range spec.Sinks {
		if s := sink.STAN; s != nil {
			secret, err := secrets.Get(ctx, "dataflow-stan-"+s.Name, metav1.GetOptions{})
			if err != nil {
				if !apierr.IsNotFound(err) {
					return err
				}
			} else {
				s.NATSURL = dfv1.StringOr(s.NATSURL, string(secret.Data["natsUrl"]))
				s.ClusterID = dfv1.StringOr(s.ClusterID, string(secret.Data["clusterId"]))
				s.SubjectPrefix = dfv1.SubjectPrefixOr(s.SubjectPrefix, dfv1.SubjectPrefix(secret.Data["subjectPrefix"]))
			}
			switch s.SubjectPrefix {
			case dfv1.SubjectPrefixNamespaceName:
				s.Subject = fmt.Sprintf("%s.%s", namespace, s.Subject)
			case dfv1.SubjectPrefixNamespacedPipelineName:
				s.Subject = fmt.Sprintf("%s.%s.%s", namespace, pipelineName, s.Subject)
			}
			sink.STAN = s
		} else if k := sink.Kafka; k != nil {
			secret, err := secrets.Get(ctx, "dataflow-kafka-"+k.Name, metav1.GetOptions{})
			if err != nil {
				if !apierr.IsNotFound(err) {
					return err
				}
			} else {
				k.Brokers = dfv1.StringsOr(k.Brokers, strings.Split(string(secret.Data["brokers"]), ","))
				k.Version = dfv1.StringOr(k.Version, string(secret.Data["version"]))
				if _, ok := secret.Data["net.tls"]; ok {
					k.NET = &dfv1.KafkaNET{TLS: &dfv1.TLS{}}
				}
			}
			sink.Kafka = k
		}
		spec.Sinks[i] = sink
	}

	return nil
}

func connectSources(ctx context.Context, toMain func([]byte) error) error {
	x := cron.New(
		cron.WithParser(cron.NewParser(cron.SecondOptional|cron.Minute|cron.Hour|cron.Dom|cron.Month|cron.Dow|cron.Descriptor)),
		cron.WithChain(cron.Recover(logger)),
	)
	go x.Run()
	closers = append(closers, func() error {
		_ = x.Stop()
		return nil
	})
	for i, source := range spec.Sources {
		if c := source.Cron; c != nil {
			logger.Info("connecting to source", "type", "cron", "schedule", c.Schedule)
			_, err := x.AddFunc(c.Schedule, func() {
				data := []byte(time.Now().Format(time.RFC3339))
				debug.Info("◷ cron →", "m", printable(data))
				withLock(func() { sourceStatues.Set(source.Name, replica, printable(data)) })
				if err := toMain(data); err != nil {
					logger.Error(err, "⚠ cron →")
					withLock(func() { sourceStatues.IncErrors(source.Name, replica, err) })
				} else {
					debug.Info("✔ cron → ", "schedule", c.Schedule)
				}
			})
			if err != nil {
				return fmt.Errorf("failed to schedule cron %q: %w", c.Schedule, err)
			}
		} else if s := source.STAN; s != nil {
			clientID := fmt.Sprintf("%s-%s-%d-source-%d", pipelineName, spec.Name, replica, i)
			logger.Info("connecting to source", "type", "stan", "url", s.NATSURL, "clusterID", s.ClusterID, "clientID", clientID, "subject", s.Subject)
			sc, err := stan.Connect(s.ClusterID, clientID, stan.NatsURL(s.NATSURL))
			if err != nil {
				return fmt.Errorf("failed to connect to stan url=%s clusterID=%s clientID=%s subject=%s: %w", s.NATSURL, s.ClusterID, clientID, s.Subject, err)
			}
			closers = append(closers, sc.Close)
			if sub, err := sc.QueueSubscribe(s.Subject, fmt.Sprintf("%s-%s", pipelineName, spec.Name), func(m *stan.Msg) {
				debug.Info("◷ stan →", "m", printable(m.Data))
				withLock(func() { sourceStatues.Set(source.Name, replica, printable(m.Data)) })
				if err := toMain(m.Data); err != nil {
					logger.Error(err, "⚠ stan →")
					withLock(func() { sourceStatues.IncErrors(source.Name, replica, err) })
				} else {
					debug.Info("✔ stan → ", "subject", s.Subject)
				}
			}, stan.DeliverAllAvailable(), stan.DurableName(clientID)); err != nil {
				return fmt.Errorf("failed to subscribe: %w", err)
			} else {
				closers = append(closers, sub.Close)
				go func() {
					defer runtimeutil.HandleCrash(runtimeutil.PanicHandlers...)
					for {
						if pending, _, err := sub.Pending(); err != nil {
							logger.Error(err, "failed to get pending", "subject", s.Subject)
						} else {
							debug.Info("setting pending", "subject", s.Subject, "pending", pending)
							withLock(func() { sourceStatues.SetPending(source.Name, uint64(pending)) })
						}
						time.Sleep(updateInterval)
					}
				}()
			}
		} else if k := source.Kafka; k != nil {
			logger.Info("connecting kafka source", "type", "kafka", "brokers", k.Brokers, "topic", k.Topic)
			config, err := newKafkaConfig(k)
			if err != nil {
				return err
			}
			config.Consumer.Return.Errors = true
			config.Consumer.Offsets.Initial = sarama.OffsetNewest
			client, err := sarama.NewClient(k.Brokers, config) // I am not giving any configuration
			if err != nil {
				return err
			}
			closers = append(closers, client.Close)
			group, err := sarama.NewConsumerGroup(k.Brokers, pipelineName+"-"+spec.Name, config)
			if err != nil {
				return fmt.Errorf("failed to create kafka consumer group: %w", err)
			}
			closers = append(closers, group.Close)
			handler := &handler{source.Name, toMain, 0}
			go func() {
				defer runtimeutil.HandleCrash(runtimeutil.PanicHandlers...)
				if err := group.Consume(ctx, []string{k.Topic}, handler); err != nil {
					logger.Error(err, "failed to create kafka consumer")
				}
			}()
			go func() {
				defer runtimeutil.HandleCrash(runtimeutil.PanicHandlers...)
				for {
					if partitions, err := client.Partitions(k.Topic); err != nil {
						logger.Error(err, "failed to get offset", "topic", k.Topic)
					} else {
						var newestOffset int64
						for _, p := range partitions {
							v, err := client.GetOffset(k.Topic, p, sarama.OffsetNewest)
							if err != nil {
								logger.Error(err, "failed to get offset", "topic", k.Topic)
							} else if v > newestOffset {
								newestOffset = v
							}
						}
						if newestOffset > handler.offset && handler.offset > 0 { // zero implies we've not processed a message yet
							pending := uint64(newestOffset - handler.offset)
							debug.Info("setting pending", "type", "kafka", "topic", k.Topic, "pending", pending)
							withLock(func() { sourceStatues.SetPending(source.Name, pending) })
						}
					}
					time.Sleep(updateInterval)
				}
			}()
		} else {
			return fmt.Errorf("source misconfigured")
		}
	}
	return nil
}

func newKafkaConfig(k *dfv1.Kafka) (*sarama.Config, error) {
	x := sarama.NewConfig()
	x.ClientID = dfv1.CtrSidecar
	if k.Version != "" {
		v, err := sarama.ParseKafkaVersion(k.Version)
		if err != nil {
			return nil, fmt.Errorf("failed to parse kafka version %q: %w", k.Version, err)
		}
		x.Version = v
	}
	if k.NET != nil {
		if k.NET.TLS != nil {
			x.Net.TLS.Enable = true
		}
	}
	return x, nil
}

func connectTo(ctx context.Context) (func([]byte) error, error) {
	in := spec.GetIn()
	if in == nil {
		logger.Info("no in interface configured")
		return func(i []byte) error {
			return fmt.Errorf("no in interface configured")
		}, nil
	} else if in.FIFO {
		logger.Info("opened input FIFO")
		fifo, err := os.OpenFile(dfv1.PathFIFOIn, os.O_WRONLY, os.ModeNamedPipe)
		if err != nil {
			return nil, fmt.Errorf("failed to open input FIFO: %w", err)
		}
		closers = append(closers, fifo.Close)
		return func(data []byte) error {
			trace.Info("◷ source → fifo")
			if _, err := fifo.Write(data); err != nil {
				return fmt.Errorf("failed to write message from source to main via FIFO: %w", err)
			}
			if _, err := fifo.Write([]byte("\n")); err != nil {
				return fmt.Errorf("failed to write new line from source to main via FIFO: %w", err)
			}
			trace.Info("✔ source → fifo")
			return nil
		}, nil
	} else if in.HTTP != nil {
		logger.Info("HTTP in interface configured")
		logger.Info("waiting for HTTP in interface to be ready")
	LOOP:
		for {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				if resp, err := http.Get("http://localhost:8080/ready"); err == nil && resp.StatusCode == 200 {
					logger.Info("HTTP in interface ready")
					break LOOP
				}
				time.Sleep(3 * time.Second)
			}
		}
		return func(data []byte) error {
			trace.Info("◷ source → http")
			resp, err := http.Post("http://localhost:8080/messages", "application/octet-stream", bytes.NewBuffer(data))
			if err != nil {
				return fmt.Errorf("failed to sent message from source to main via HTTP: %w", err)
			}
			if resp.StatusCode >= 300 {
				return fmt.Errorf("failed to sent message from source to main via HTTP: %s", resp.Status)
			}
			trace.Info("✔ source → http")
			return nil
		}, nil
	} else {
		return nil, fmt.Errorf("in interface misconfigured")
	}
}

func connectOut(toSink func([]byte) error) {
	logger.Info("FIFO out interface configured")
	go func() {
		defer runtimeutil.HandleCrash(runtimeutil.PanicHandlers...)
		err := func() error {
			fifo, err := os.OpenFile(dfv1.PathFIFOOut, os.O_RDONLY, os.ModeNamedPipe)
			if err != nil {
				return fmt.Errorf("failed to open output FIFO: %w", err)
			}
			defer fifo.Close()
			logger.Info("opened output FIFO")
			scanner := bufio.NewScanner(fifo)
			for scanner.Scan() {
				trace.Info("◷ fifo → sink")
				if err := toSink(scanner.Bytes()); err != nil {
					return fmt.Errorf("failed to send message from main to sink: %w", err)
				}
				trace.Info("✔ fifo → sink")
			}
			if err = scanner.Err(); err != nil {
				return fmt.Errorf("scanner error: %w", err)
			}
			return nil
		}()
		if err != nil {
			logger.Error(err, "failed to received message from FIFO")
			os.Exit(1)
		}
	}()
	logger.Info("HTTP out interface configured")
	http.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		data, err := ioutil.ReadAll(r.Body)
		if err != nil {
			logger.Error(err, "failed to read message body from main via HTTP")
			w.WriteHeader(500)
			return
		}
		trace.Info("◷ http → sink")
		if err := toSink(data); err != nil {
			logger.Error(err, "failed to send message from main to sink")
			w.WriteHeader(500)
			return
		}
		trace.Info("✔ http → sink")
		w.WriteHeader(200)
	})
	go func() {
		defer runtimeutil.HandleCrash(runtimeutil.PanicHandlers...)
		logger.Info("starting HTTP server")
		err := http.ListenAndServe(":3569", nil)
		if err != nil {
			logger.Error(err, "failed to listen-and-server")
			os.Exit(1)
		}
	}()
}

func connectSink() (func([]byte) error, error) {
	var toSinks []func([]byte) error
	for i, sink := range spec.Sinks {
		if s := sink.STAN; s != nil {
			clientID := fmt.Sprintf("%s-%s-%d-sink-%d", pipelineName, spec.Name, replica, i)
			logger.Info("connecting sink", "type", "stan", "url", s.NATSURL, "clusterID", s.ClusterID, "clientID", clientID, "subject", s.Subject)
			sc, err := stan.Connect(s.ClusterID, clientID, stan.NatsURL(s.NATSURL))
			if err != nil {
				return nil, fmt.Errorf("failed to connect to stan url=%s clusterID=%s clientID=%s subject=%s: %w", s.NATSURL, s.ClusterID, clientID, s.Subject, err)
			}
			closers = append(closers, sc.Close)
			toSinks = append(toSinks, func(m []byte) error {
				withLock(func() { sinkStatues.Set(sink.Name, replica, printable(m)) })
				debug.Info("◷ → stan", "subject", s.Subject, "m", printable(m))
				return sc.Publish(s.Subject, m)
			})
		} else if k := sink.Kafka; k != nil {
			logger.Info("connecting sink", "type", "kafka", "brokers", k.Brokers, "topic", k.Topic, "version", k.Version)
			config, err := newKafkaConfig(k)
			if err != nil {
				return nil, err
			}
			config.Producer.Return.Successes = true
			producer, err := sarama.NewSyncProducer(k.Brokers, config)
			if err != nil {
				return nil, fmt.Errorf("failed to create kafka producer: %w", err)
			}
			closers = append(closers, producer.Close)
			toSinks = append(toSinks, func(m []byte) error {
				withLock(func() { sinkStatues.Set(sink.Name, replica, printable(m)) })
				debug.Info("◷ → kafka", "topic", k.Topic, "m", printable(m))
				_, _, err := producer.SendMessage(&sarama.ProducerMessage{
					Topic: k.Topic,
					Value: sarama.ByteEncoder(m),
				})
				return err
			})
		} else if l := sink.Log; l != nil {
			logger.Info("connecting sink", "type", "log")
			toSinks = append(toSinks, func(m []byte) error {
				withLock(func() { sinkStatues.Set(sink.Name, replica, printable(m)) })
				logger.Info(string(m), "type", "log")
				return nil
			})
		} else {
			return nil, fmt.Errorf("sink misconfigured")
		}
	}
	return func(m []byte) error {
		for _, s := range toSinks {
			if err := s(m); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

// format or redact message
func printable(m []byte) string {
	return apiutil.Printable(string(m))
}
