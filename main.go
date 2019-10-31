package main

import (
    "fmt"
    "encoding/json"
    "os"
    "os/signal"

    list "container/list"
    "github.com/Shopify/sarama"
    "github.com/mistsys/mist_go_utils/cloud"
)

// sorted list for edges, sorting done on ep-term timestamp. In future we can
// make this user defined
var edge_list *list.List
// hash map for storing the location of each device in the list. Just like in an LRU cache
// implementation
var edge_ptr_map map[string]map[string]interface{}


func main() {
    edge_list = list.New()
    edge_ptr_map = make(map[string]map[string]interface{})
    fmt.Println("Welcome to cadence!")
    fmt.Printf("Broker list:%+v\n", cloud.KAFKA_BROKERS)
    conf := sarama.NewConfig()
    conf.ClientID = "cadence"
    master, err := sarama.NewConsumer(cloud.KAFKA_BROKERS, conf)
    if err != nil {
        panic(err)
    }
//    st, _ := master.Topics()
    consumer, errors := consume("marvis-edge-to-cloud-staging", master)
    signals := make(chan os.Signal, 1)
    signal.Notify(signals, os.Interrupt)

    msgCount := 0
    doneCh := make(chan struct{})
    go func() {
        for {
            select {
                case msg := <-consumer:
                    msgCount++
                    edge_msg := make(map[string]interface{})
                    err := json.Unmarshal([]byte(msg.Value), &edge_msg)
                    if err != nil {
                        panic(err)
                    }
                    fmt.Printf("Recv msgs len:%d k:%+v msg:%+v\n\n", edge_list.Len(), string(msg.Key), edge_msg["InfoFromTerminator"].(map[string]interface{})["Timestamp"])
                    edge_list.PushFront(edge_msg)
                    if edge_cur_msg, ok := edge_ptr_map[edge_msg["ID"].(string)]; ok {
                        cur_ts := edge_cur_msg["InfoFromTerminator"].(map[string]interface{})["Timestamp"].(string)
                        if cur_ts < edge_msg["InfoFromTerminator"].(map[string]interface{})["Timestamp"].(string) {
                            edge_ptr_map[edge_msg["ID"].(string)] = edge_msg
                        }
                    } else {
                        edge_ptr_map[edge_msg["ID"].(string)] = edge_msg
                    }
                    fmt.Printf("oooooooooooo k:%+v\n\n\n", edge_list.Front())
                case consumerError := <-errors:
                    msgCount++
                    fmt.Printf("Recv errors ", string(consumerError.Topic), string(consumerError.Partition), consumerError.Err)
                    doneCh <- struct{}{}
                case <-signals:
                    fmt.Printf("Interrupt is detected")
                    doneCh <- struct{}{}
                    os.Exit(1)
            }
        }
    }()
    <-doneCh
}

func consume(topic string, master sarama.Consumer) (chan *sarama.ConsumerMessage, chan *sarama.ConsumerError) {
    consumers := make(chan *sarama.ConsumerMessage)
    errors := make(chan *sarama.ConsumerError)
    partitions, _ := master.Partitions(topic)
    fmt.Printf("Topic:%s # partitions:%d\n", topic, len(partitions))

    consumer, err := master.ConsumePartition(topic, partitions[0], sarama.OffsetNewest)
    if err != nil {
        panic(err)
    }
    fmt.Printf("Lets start consuming..\n")
    go func(topic string, consumer sarama.PartitionConsumer) {
        for {
            select {
                case consumerError := <-consumer.Errors():
                    errors <- consumerError
                case msg := <-consumer.Messages():
                    consumers <- msg
            }
        }
    } (topic, consumer)
    return consumers, errors
}
