package main

import (
    "fmt"
    "os"
    "os/signal"

    "github.com/Shopify/sarama"
    "github.com/mistsys/mist_go_utils/cloud"
)

func main() {
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
                    fmt.Printf("Recv msgs k:%+v msg:%+v\n\n", string(msg.Key), string(msg.Value))
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
