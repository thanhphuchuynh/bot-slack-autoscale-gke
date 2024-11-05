package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/joho/godotenv"
	"github.com/robfig/cron/v3"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type TypeQuestionType string

const (
	QuestionTypeYesNoScaleDown TypeQuestionType = "yes_no_scale_down"
	QuestionTypeYesNoScaleUp   TypeQuestionType = "yes_no_scale_up"
)

// create a cron job to send a yes/no question to slack, everyday at 20:00 and 7h00
// 20h scale down the nodepool gke resize 0 and 7h00 scale up the nodepool gke resize 1

// method send a yes/no question to slack and return the answer
func sendYesNoQuestion(client *slack.Client, channelID string, question string, typeQuestion TypeQuestionType) (string, error) {
	// Create the message with buttons
	attachment := slack.Attachment{
		Text:       question,
		CallbackID: string(typeQuestion),
		Actions: []slack.AttachmentAction{
			{
				Name:  "yes",
				Text:  "Yes",
				Type:  "button",
				Value: "yes",
			},
			{
				Name:  "no",
				Text:  "No",
				Type:  "button",
				Value: "no",
			},
		},
	}

	// Send the message and store the timestamp
	_, timestamp, err := client.PostMessage(
		channelID,
		slack.MsgOptionAttachments(attachment),
	)
	if err != nil {
		return "", err
	}

	// Store the timestamp in a global variable or a map
	// For simplicity, we'll use a global variable here
	lastQuestionTimestamp = timestamp

	return "Response pending", nil
}

// Global variable to store the timestamp of the last question
var lastQuestionTimestamp string

func main() {
	// Load Env variables from .dot file
	godotenv.Load(".env")

	token := os.Getenv("SLACK_AUTH_TOKEN")
	appToken := os.Getenv("SLACK_APP_TOKEN")
	channelID := os.Getenv("SLACK_CHANNEL_ID")
	// Create a new client to slack by giving token
	// Set debug to true while developing
	// Also add a ApplicationToken option to the client
	client := slack.New(token, slack.OptionDebug(true), slack.OptionAppLevelToken(appToken))
	// go-slack comes with a SocketMode package that we need to use that accepts a Slack client and outputs a Socket mode client instead
	socketClient := socketmode.New(
		client,
		socketmode.OptionDebug(true),
		// Option to set a custom logger
		socketmode.OptionLog(log.New(os.Stdout, "socketmode: ", log.Lshortfile|log.LstdFlags)),
	)

	// Create a new cron scheduler
	c := cron.New()

	// Schedule the 20:00 job
	// c.AddFunc("0 20 * * *", func() {
	c.AddFunc("33 0 * * *", func() {
		question := "Do you want to scale down the GKE nodepool to 0?"
		response, err := sendYesNoQuestion(client, channelID, question, QuestionTypeYesNoScaleDown)
		if err != nil {
			log.Printf("Error sending question: %v\n", err)
		}
		log.Printf("Sent question at 20:00, response: %s\n", response)
	})

	// Schedule the 07:00 job
	// c.AddFunc("0 7 * * *", func() {
	// c.AddFunc("17 0 * * *", func() {
	// 	question := "Do you want to scale up the GKE nodepool to 1?"
	// 	response, err := sendYesNoQuestion(client, channelID, question)
	// 	if err != nil {
	// 		log.Printf("Error sending question: %v\n", err)
	// 	}
	// 	log.Printf("Sent question at 07:00, response: %s\n", response)
	// })

	// Start the cron scheduler
	c.Start()

	// sendYesNoQuestion(client, channelID, "Are you sure you want to do this?")

	// Create a context that can be used to cancel goroutine
	ctx, cancel := context.WithCancel(context.Background())
	// Make this cancel called properly in a real program , graceful shutdown etc
	defer cancel()

	go func(ctx context.Context, client *slack.Client, socketClient *socketmode.Client) {
		// Create a for loop that selects either the context cancellation or the events incomming
		for {
			select {
			// inscase context cancel is called exit the goroutine
			case <-ctx.Done():
				log.Println("Shutting down socketmode listener")
				return
			case event := <-socketClient.Events:
				// We have a new Events, let's type switch the event
				// Add more use cases here if you want to listen to other events.
				switch event.Type {
				// handle EventAPI events
				case socketmode.EventTypeEventsAPI:
					// The Event sent on the channel is not the same as the EventAPI events so we need to type cast it
					eventsAPIEvent, ok := event.Data.(slackevents.EventsAPIEvent)
					if !ok {
						log.Printf("Could not type cast the event to the EventsAPIEvent: %v\n", event)
						continue
					}
					// We need to send an Acknowledge to the slack server
					socketClient.Ack(*event.Request)
					// Now we have an Events API event, but this event type can in turn be many types, so we actually need another type switch
					log.Println(eventsAPIEvent)
				case socketmode.EventTypeInteractive:
					interaction, ok := event.Data.(slack.InteractionCallback)
					if !ok {
						log.Printf("Could not type cast the event to the InteractionCallback: %v\n", event)
						continue
					}

					// Acknowledge the event
					socketClient.Ack(*event.Request)

					// Handle the question
					switch TypeQuestionType(interaction.CallbackID) {
					case QuestionTypeYesNoScaleDown:
						response := interaction.ActionCallback.AttachmentActions[0].Value
						if response == "yes" {
							_, _, _, err := client.UpdateMessage(
								interaction.Channel.ID,
								lastQuestionTimestamp,
								slack.MsgOptionText("The action has been confirmed and is being processed.", true),
								slack.MsgOptionAttachments(), // Remove the empty slice
								slack.MsgOptionAttachments(slack.Attachment{}),
							)
							if err != nil {
								log.Printf("Error updating message: %v\n", err)
							}
							// Perform GKE nodepool resize action
							go performGKEAction("Do you want to scale down the GKE nodepool to 0?")
						} else {
							// Optionally, update the message for "No" response as well
							_, _, _, err := client.UpdateMessage(
								interaction.Channel.ID,
								lastQuestionTimestamp,
								slack.MsgOptionText("The action has been cancelled.", true),
								slack.MsgOptionAttachments(), // Remove the empty slice
								slack.MsgOptionAttachments(slack.Attachment{}),
							)
							if err != nil {
								log.Printf("Error updating message: %v\n", err)
							}
						}

					case QuestionTypeYesNoScaleUp:
						response := interaction.ActionCallback.AttachmentActions[0].Value
						if response == "yes" {
							_, _, _, err := client.UpdateMessage(
								interaction.Channel.ID,
								lastQuestionTimestamp,
								slack.MsgOptionText("The action has been confirmed and is being processed.", true),
								slack.MsgOptionAttachments(), // Remove the empty slice
								slack.MsgOptionAttachments(slack.Attachment{}),
							)
							if err != nil {
								log.Printf("Error updating message: %v\n", err)
							}
							// Perform GKE nodepool resize action
							go performGKEAction("Do you want to scale up the GKE nodepool to 1?")
						} else {
							// Optionally, update the message for "No" response as well
							_, _, _, err := client.UpdateMessage(
								interaction.Channel.ID,
								lastQuestionTimestamp,
								slack.MsgOptionText("The action has been cancelled.", true),
								slack.MsgOptionAttachments(), // Remove the empty slice
								slack.MsgOptionAttachments(slack.Attachment{}),
							)
							if err != nil {
								log.Printf("Error updating message: %v\n", err)
							}
						}
					}

					// Handle the yes/no response
					if interaction.CallbackID == "yes_no_question" {
						response := interaction.ActionCallback.AttachmentActions[0].Value
						log.Printf("Received response: %s\n", response)

						if response == "yes" {
							// Update the original message
							_, _, _, err := client.UpdateMessage(
								interaction.Channel.ID,
								lastQuestionTimestamp,
								slack.MsgOptionText("The action has been confirmed and is being processed.", true),
								slack.MsgOptionAttachments(), // Remove the empty slice
								slack.MsgOptionAttachments(slack.Attachment{}),
							)
							if err != nil {
								log.Printf("Error updating message: %v\n", err)
							}

							// Perform GKE nodepool resize action
							go performGKEAction(interaction.Message.Text)
						} else {
							// Optionally, update the message for "No" response as well
							_, _, _, err := client.UpdateMessage(
								interaction.Channel.ID,
								lastQuestionTimestamp,
								slack.MsgOptionText("The action has been cancelled.", true),
								slack.MsgOptionAttachments(), // Remove the empty slice
								slack.MsgOptionAttachments(slack.Attachment{}),
							)
							if err != nil {
								log.Printf("Error updating message: %v\n", err)
							}
						}
					}
				}

			}
		}
	}(ctx, client, socketClient)

	socketClient.Run()
}

func performGKEAction(question string) {
	clusterName := os.Getenv("YOUR_CLUSTER_NAME")
	nodePoolName := os.Getenv("YOUR_NODEPOOL_NAME")
	zone := os.Getenv("YOUR_ZONE")
	project := os.Getenv("YOUR_PROJECT")

	var cmd *exec.Cmd
	if question == "Do you want to scale down the GKE nodepool to 0?" {
		cmd = exec.Command("gcloud", "container", "clusters", "resize", clusterName, "--node-pool", nodePoolName, "--num-nodes", "0", "--zone", zone, "--project", project, "--quiet")
	} else if question == "Do you want to scale up the GKE nodepool to 1?" {
		cmd = exec.Command("gcloud", "container", "clusters", "resize", clusterName, "--node-pool", nodePoolName, "--num-nodes", "1", "--zone", zone, "--project", project, "--quiet")
	} else {
		log.Printf("Unknown question: %s\n", question)
		return
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Error executing gcloud command: %v\n", err)
		log.Printf("Command output: %s\n", string(output))
	} else {
		log.Printf("GKE action completed successfully: %s\n", string(output))
	}
}

// func performGKEActionBySDK(question string) {
// 	ctx := context.Background()

// 	// Create a GKE client
// 	client, err := container.NewClusterManagerClient(ctx)
// 	if err != nil {
// 		log.Printf("Error creating GKE client: %v\n", err)
// 		return
// 	}
// 	defer client.Close()

// 	// Get environment variables
// 	projectID := os.Getenv("YOUR_PROJECT_ID")
// 	zone := os.Getenv("YOUR_ZONE")
// 	clusterName := os.Getenv("YOUR_CLUSTER_NAME")
// 	nodePoolName := os.Getenv("YOUR_NODEPOOL_NAME")

// 	// Determine the desired number of nodes based on the question
// 	var desiredNodes int32
// 	if question == "Do you want to scale down the GKE nodepool to 0?" {
// 		desiredNodes = 0
// 	} else if question == "Do you want to scale up the GKE nodepool to 1?" {
// 		desiredNodes = 1
// 	} else {
// 		log.Printf("Unknown question: %s\n", question)
// 		return
// 	}

// 	// Create the request to set the node pool size
// 	req := &containerpb.SetNodePoolSizeRequest{
// 		ProjectId:  projectID,
// 		Zone:       zone,
// 		ClusterId:  clusterName,
// 		NodePoolId: nodePoolName,
// 		NodeCount:  desiredNodes,
// 	}

// 	// Call the SetNodePoolSize method
// 	op, err := client.SetNodePoolSize(ctx, req)
// 	if err != nil {
// 		log.Printf("Error setting node pool size: %v\n", err)
// 		return
// 	}

// 	// Wait for the operation to complete
// 	err = op.Wait(ctx)
// 	if err != nil {
// 		log.Printf("Error waiting for operation to complete: %v\n", err)
// 		return
// 	}

// 	log.Printf("Successfully scaled node pool %s to %d nodes\n", nodePoolName, desiredNodes)
// }

func mainnew() {
	fmt.Println("Get Kubernetes pods")

	userHomeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("error getting user home dir: %v\n", err)
		os.Exit(1)
	}
	kubeConfigPath := filepath.Join(userHomeDir, ".kube", "config")
	fmt.Printf("Using kubeconfig: %s\n", kubeConfigPath)

	kubeConfig, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	if err != nil {
		fmt.Printf("error getting Kubernetes config: %v\n", err)
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		fmt.Printf("error getting Kubernetes clientset: %v\n", err)
		os.Exit(1)
	}

	node, err := clientset.CoreV1().Nodes().List(context.Background(), v1.ListOptions{})
	if err != nil {
		fmt.Printf("error getting node: %v\n", err)
		os.Exit(1)
	}
	for _, pod := range node.Items {
		fmt.Printf("Pod name: %s\n", pod.Name)
	}

	// scale down the nodepool
	// Scale down the node pool to 0 nodes
	nodePoolName := "default-pool" // Replace with your actual node pool name
	desiredNodes := int32(0)

	// Get the node pool
	nodePool, err := clientset.CoreV1().Nodes().List(context.Background(), v1.ListOptions{
		LabelSelector: fmt.Sprintf("cloud.google.com/gke-nodepool=%s", nodePoolName),
	})
	if err != nil {
		fmt.Printf("error getting node pool: %v\n", err)
		os.Exit(1)
	}

	// Cordon and drain each node before deletion
	for _, node := range nodePool.Items {
		// Cordon the node
		node.Spec.Unschedulable = true
		_, err = clientset.CoreV1().Nodes().Update(context.Background(), &node, v1.UpdateOptions{})
		if err != nil {
			fmt.Printf("error cordoning node %s: %v\n", node.Name, err)
			continue
		}

		// Drain the node
		pods, err := clientset.CoreV1().Pods("").List(context.Background(), v1.ListOptions{
			FieldSelector: fmt.Sprintf("spec.nodeName=%s", node.Name),
		})
		if err != nil {
			fmt.Printf("error listing pods on node %s: %v\n", node.Name, err)
			continue
		}

		for _, pod := range pods.Items {
			err = clientset.CoreV1().Pods(pod.Namespace).Delete(context.Background(), pod.Name, v1.DeleteOptions{})
			if err != nil {
				fmt.Printf("error deleting pod %s: %v\n", pod.Name, err)
			}
		}
	}

	// Delete the nodes
	for _, node := range nodePool.Items {
		err = clientset.CoreV1().Nodes().Delete(context.Background(), node.Name, v1.DeleteOptions{})
		if err != nil {
			fmt.Printf("error deleting node %s: %v\n", node.Name, err)
		}
	}

	fmt.Printf("Successfully scaled down node pool %s to %d nodes\n", nodePoolName, desiredNodes)

	// scale up node
	// desiredNodes = int32(1)

	// // Get the node pool
	// nodePool, err := clientset.CoreV1().Nodes().List(context.Background(), v1.ListOptions{
	// 	LabelSelector: fmt.Sprintf("cloud.google.com/gke-nodepool=%s", nodePoolName),
	// })
	// if err != nil {
	// 	fmt.Printf("error getting node pool: %v\n", err)
	// 	os.Exit(1)
	// }
	// // Scale up the node pool by creating new nodes
	// for i := len(nodePool.Items); i < int(desiredNodes); i++ {
	// 	// Create a new node
	// 	node := &corev1.Node{
	// 		ObjectMeta: v1.ObjectMeta{
	// 			GenerateName: fmt.Sprintf("%s-", nodePoolName),
	// 			Labels: map[string]string{
	// 				"cloud.google.com/gke-nodepool": nodePoolName,
	// 			},
	// 		},
	// 	}
	// 	_, err = clientset.CoreV1().Nodes().Create(context.Background(), node, v1.CreateOptions{})
	// 	if err != nil {
	// 		fmt.Printf("error creating node: %v\n", err)
	// 		continue
	// 	}
	// }

	// fmt.Printf("Successfully scaled up node pool %s to %d nodes\n", nodePoolName, desiredNodes)
}
