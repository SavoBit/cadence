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
var edge_ptr_map map[string]*list.Element


func main() {
    edge_list = list.New()
    edge_ptr_map = make(map[string]*list.Element)
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
                    if edge_ptr, ok := edge_ptr_map[edge_msg["ID"].(string)]; ok {
                        // check timestamp on the newly recvd msg and compare it to what
                        // we have in the list for this edge. If we recv a more recent msg
                        // update the list and put the node at the tail of the list
                        // remember, the tail has the edges with the most recent msgs
                        fmt.Printf("found stuff %+v\n\n", edge_ptr.Value.(map[string]interface{})["InfoFromTerminator"].(map[string]interface{})["Timestamp"])
                        cur_info := edge_ptr.Value.(map[string]interface{})["InfoFromTerminator"]

                        cur_ts := cur_info.(map[string]interface{})["Timestamp"].(string)
                        new_info := edge_msg["InfoFromTerminator"]
                        if cur_ts < new_info.(map[string]interface{})["Timestamp"].(string) {
                            //edge_ptr_map[edge_msg["ID"].(string)] = edge_msg
                            fmt.Printf("resetting ... %+v", edge_ptr)
                        }

                    } else {
                        // New edge, add in the map and enter a new node in the list
                        e := edge_list.PushFront(edge_msg)
                        edge_ptr_map[edge_msg["ID"].(string)] = e
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
