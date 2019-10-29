package main

import (
    "fmt"
    "encoding/json"
    "os"
    "os/signal"

    "github.com/Shopify/sarama"
    lru "github.com/hashicorp/golang-lru"
    "github.com/mistsys/mist_go_utils/cloud"
)

var aps_cache *lru.Cache



func main() {
    aps_cache, _ = lru.New(128)
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
                    fmt.Printf("Recv msgs len:%d k:%+v msg:%+v\n\n", aps_cache.Len(), string(msg.Key), edge_msg)
                    aps_cache.Add(edge_msg["ID"], edge_msg)
                    k, v, _ := aps_cache.GetOldest()
                    fmt.Printf("oooooooooooo k:%+v v:%+v\n\n\n", k, v)
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

    consumer, err := master.ConsumePartition(topic, partitions[0], sarama.OffsetOldest)
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
