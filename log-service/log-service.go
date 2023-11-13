package main

import (
	"context"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type PodLog struct {
	ctx       context.Context
	clientset *kubernetes.Clientset
	nc        *nats.Conn
	js        jetstream.JetStream
	name      string
	container string
	namespace string
}

func GetPodLogs(logStruct PodLog) {
	count := int64(100)
	podLogOptions := v1.PodLogOptions{
		Follow:    true,
		TailLines: &count,
		Container: logStruct.container,
	}

	podLogRequest := logStruct.clientset.CoreV1().
		Pods(logStruct.namespace).
		GetLogs(logStruct.name, &podLogOptions)
	stream, err := podLogRequest.Stream(context.TODO())
	if err != nil {
		log.Printf("%s:%s -> %s", logStruct.namespace, logStruct.name, err)
		return
	}
	defer stream.Close()

	for {
		buf := make([]byte, 2000)
		numBytes, err := stream.Read(buf)
		if numBytes == 0 {
			time.Sleep(1 * time.Second)
			continue
		}

		if err == io.EOF {
			break
		}

		if err != nil {
			log.Printf("%s:%s -> %s", logStruct.namespace, logStruct.name, err)
			return
		}

		message := string(buf[:numBytes])

		if logStruct.nc.Status() == nats.CONNECTED {
			if _, err := logStruct.js.Publish(logStruct.ctx, "ls."+logStruct.name, []byte(message)); err != nil {
				log.Fatalf("JetStream publish: %s\n", err)
			}
		}

	}
}

func watchPods(ctx context.Context, clientset *kubernetes.Clientset, nc *nats.Conn, js jetstream.JetStream) {
	var wg sync.WaitGroup
	defer wg.Done()
	timeOut := int64(60)
	watcher, err := clientset.CoreV1().Pods("").Watch(context.Background(), metav1.ListOptions{TimeoutSeconds: &timeOut})

	if err != nil {
		log.Fatal(err)
	}

	// watch for new pods
	for event := range watcher.ResultChan() {
		item := event.Object.(*v1.Pod)

		switch event.Type {
		case watch.Added:
			for _, container := range item.Spec.Containers {
				logStruct := PodLog{
					ctx:       ctx,
					name:      item.Name,
					container: container.Name,
					clientset: clientset,
					namespace: item.Namespace,
					nc:        nc,
					js:        js,
				}
				wg.Add(1)
				go GetPodLogs(logStruct)
			}
		}
	}
	wg.Wait()
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	var wg sync.WaitGroup
	defer wg.Done()
	ns := os.Getenv("POD_NAMESPACE")

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Pod-Log-Service running in namespace %s", ns)

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	srv, err := clientset.CoreV1().Services(ns).Get(context.TODO(), "pod-log-service-nats", metav1.GetOptions{})
	if err != nil {
		log.Fatal(err)
	}

	nc, err := nats.Connect(srv.Spec.ClusterIP + ":4222")
	if err != nil {
		log.Fatalf("NATS: %s : %s", srv.Spec.ClusterIP, err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatal(err)
	}

	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     "log-service-test",
		Subjects: []string{"ls.*"},
	})
	if err != nil {
		log.Fatal(err)
	}

	wg.Add(1)
	go watchPods(ctx, clientset, nc, js)
	wg.Wait()

}
