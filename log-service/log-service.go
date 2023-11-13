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
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func GetPodLogs(clientset *kubernetes.Clientset, wg *sync.WaitGroup, ctx context.Context, nc *nats.Conn, js jetstream.JetStream, namespace string, podName string, containerName string, follow bool) {
	defer wg.Done()

	count := int64(100)
	podLogOptions := v1.PodLogOptions{
		Follow:    true,
		TailLines: &count,
		Container: containerName,
	}

	podLogRequest := clientset.CoreV1().
		Pods(namespace).
		GetLogs(podName, &podLogOptions)
	stream, err := podLogRequest.Stream(context.TODO())
	if err != nil {
		log.Printf("%s:%s -> %s", namespace, podName, err)
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
			log.Printf("%s:%s -> %s", namespace, podName, err)
			return
		}

		message := string(buf[:numBytes])

		if nc.Status() == nats.CONNECTED {
			if _, err := js.Publish(ctx, "ls."+podName, []byte(message)); err != nil {
				log.Fatalf("JetStream publish: %s\n", err)
			}
		}

	}

	return
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	var wg sync.WaitGroup
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
		log.Fatalf("NATS: %s : %s", &srv.Spec.ClusterIP, err)
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

	pods, err := clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("There are %d pods in the cluster\n", len(pods.Items))
	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			wg.Add(1)
			go GetPodLogs(clientset, &wg, ctx, nc, js, pod.Namespace, pod.Name, container.Name, false)
		}
	}
	wg.Wait()

}
