package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	list "container/list"
	"github.com/Shopify/sarama"
	"github.com/mistsys/cadence/stats"
	"github.com/mistsys/cadence/ttstats"
	"github.com/mistsys/mist_go_utils/cloud"
	"github.com/mistsys/mist_go_utils/flag"
	"github.com/mistsys/mist_go_utils/timeparse"
	"github.com/mistsys/protobuf3/protobuf3"
)

// Application name to pass in events
var APP_NAME string

// lets keep track of bad msgs
var BAD_MSGS int64

// track total correct msgs
var PROCESSED_MSGS int64

// sorted list for edges, sorting done on ep-term timestamp. In future we can
// make this user defined
var edge_list *list.List

// sarama producer
var producer sarama.SyncProducer

// hash map for storing the location of each device in the list. Just like in an LRU cache
// implementation
var edge_ptr_map map[string]*list.Element

// max time we have seen on the stream. We DONT want to rely on current time
// and instead our concept of time is what we've seen.
// Strange? Imagine if you are clearing lag for old msgs.
var CURRENT_STREAM_TIME int64

// topic which is sending the heart beats for cadence to determine status
var HEART_BEAT_TOPIC string

// full name for heart beat topic including ENV
var HB_TOPIC_FULLNAME string

func getTimeStamp(e *list.Element) int64 {
	cur_ts := e.Value.(map[string]interface{})["InfoFromTerminator"]
	ts := cur_ts.(map[string]interface{})["Timestamp"].(string)
	t, _ := timeparse.Parse(ts)
	return t.Unix()
}

func getEpoch(ts string) int64 {
	t, _ := timeparse.Parse(ts)
	return t.Unix()
}

func findPosition(ts int64) (e *list.Element) {
	for e := edge_list.Back(); e != nil; e = e.Prev() {
		cur_ts := e.Value.(EdgeState).TimeStamp
		if ts > cur_ts {
			return e
		}
	}
	return e
}

func checkEdgeStatus(producer sarama.SyncProducer) {
	for {
		for e := edge_list.Front(); e != nil; e = e.Next() {
			cur_ts := e.Value.(EdgeState).TimeStamp
			status := e.Value.(EdgeState).CadenceStatus
			id := e.Value.(EdgeState).ID
			org_id := e.Value.(EdgeState).OrgID
			fmt.Printf("ID:%s stream_time:%d last beat:%d status:%s\n", id, CURRENT_STREAM_TIME, cur_ts, status)
			if CURRENT_STREAM_TIME > 5+cur_ts && status == "UP" {
				fmt.Printf("EDGE is DOWN!!!!\n\n")
				////////////////////////////////////////////////////////////////////
				send_event_msg(producer, "down", id, org_id, APP_NAME)
				////////////////////////////////////////////////////////////////////
				new_state := EdgeState{
					CadenceStatus: "DOWN",
					ID:            id,
					OrgID:         org_id,
					TimeStamp:     cur_ts,
				}
				e.Value = new_state
			}
		}
		fmt.Printf("\n\n")
		time.Sleep(1 * time.Second)
	}
}

func sanityChecker() {
	for {
		var last_ts int64
		last_ts = -1
		for e := edge_list.Front(); e != nil; e = e.Next() {
			cur_ts := e.Value.(EdgeState).TimeStamp
			if last_ts != -1 {
				if cur_ts < last_ts {
					fmt.Printf("Crap!!! %s %s", last_ts, cur_ts)
					panic("Crap!!!")
				}
			} else {
				last_ts = cur_ts
			}
		}
		time.Sleep(1 * time.Second)
	}
}

type MXEdgeMsg struct {
	Time     int64  `json:"Time"`
	OrgID    string `json:"OrgID"`
	MXEdgeID string `json:"ID"`
	AppName  string `json:"AppName"`
}

type MXEdgeEvent struct {
	MsgType  string `json:"MsgType"`
	Time     string `json:"Time"`
	OrgID    string `json:"OrgID"`
	MXEdgeID string `json:"ID"`
	S3Path   string `json:"S3Path"`
	AppName  string `json:"AppName"`

	encoded []byte
	err     error
}

type BasicInfo interface {
	GetID() string
	GetOrgID() string
}

type TTStatsMsg struct {
	msg ttstats.TTStats
}

type MXAgentMsg struct {
	msg map[string]interface{}
}

func (m MXAgentMsg) GetID() string {
	new_info := m.msg["InfoFromTerminator"]
	return new_info.(map[string]interface{})["ID"].(string)
}

func (m MXAgentMsg) GetOrgID() string {
	new_info := m.msg["InfoFromTerminator"]
	return new_info.(map[string]interface{})["OrgID"].(string)
}

func (m TTStatsMsg) GetID() string {
	return m.msg.InfoFromTerminator.ID
}

func (m TTStatsMsg) GetOrgID() string {
	return m.msg.InfoFromTerminator.OrgID.String()
}

func (ale *MXEdgeEvent) Length() int {
	ale.ensureEncoded()
	return len(ale.encoded)
}

func (ale *MXEdgeEvent) ensureEncoded() {
	if ale.encoded == nil && ale.err == nil {
		ale.encoded, ale.err = json.Marshal(ale)
	}
}

func (ale *MXEdgeEvent) Encode() ([]byte, error) {
	ale.ensureEncoded()
	return ale.encoded, ale.err
}

type EdgeState struct {
	ID            string
	OrgID         string
	TimeStamp     int64
	CadenceStatus string
}

func (s EdgeState) SetCadenceStatus(st string) {
	s.CadenceStatus = st
}

// Consumer represents a Sarama consumer group consumer
type Consumer struct {
	ready chan bool
}

// Setup is run at the beginning of a new session, before ConsumeClaim
func (consumer *Consumer) Setup(sarama.ConsumerGroupSession) error {
	// Mark the consumer as ready
	close(consumer.ready)
	return nil
}

// Cleanup is run at the end of a session, once all ConsumeClaim goroutines have exited
func (consumer *Consumer) Cleanup(sarama.ConsumerGroupSession) error {
	return nil
}

// ConsumeClaim must start a consumer loop of ConsumerGroupClaim's Messages().
func (consumer *Consumer) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {

	// NOTE:
	// Do not move the code below to a goroutine.
	// The `ConsumeClaim` itself is called within a goroutine, see:
	// https://github.com/Shopify/sarama/blob/master/consumer_group.go#L27-L29
	for message := range claim.Messages() {
		//log.Printf("Message claimed: value = %s, timestamp = %v, topic = %s", string(message.Value), message.Timestamp, message.Topic)
		processMsg(message)
		session.MarkMessage(message, "")
	}

	return nil
}

func extractInfo(msg *sarama.ConsumerMessage) *MXEdgeMsg {
	var org_id string
	var cur_id string
	var msg_ts string
	var new_ts int64
	if strings.HasPrefix(HB_TOPIC_FULLNAME, "tt-stats-") {
		var m ttstats.TTStats
		if err := protobuf3.Unmarshal(msg.Value, &m); err != nil {
			panic(err)
		}
		fmt.Printf("------------------- %+v\n\n\n", m.InfoFromTerminator)
		org_id = m.InfoFromTerminator.OrgID.String()
		cur_id = m.InfoFromTerminator.ID
		msg_ts = m.InfoFromTerminator.Timestamp.Format("2019-12-02T23:01:04.939683607Z")
		new_ts = m.InfoFromTerminator.Timestamp.Unix()
	} else if strings.HasPrefix(HB_TOPIC_FULLNAME, "ap-stats-full-") {
		var m stats.APStats
		if err := protobuf3.Unmarshal(msg.Value, &m); err != nil {
			BAD_MSGS += 1
			fmt.Printf("BAD msgs %d\n", BAD_MSGS)
			return nil
		}
		PROCESSED_MSGS += 1
		fmt.Printf("------------------- msgs:%d %+v\n", PROCESSED_MSGS, m.InfoFromTerminator)
	} else {
		edge_m := make(map[string]interface{})
		err := json.Unmarshal([]byte(msg.Value), &edge_m)
		if err != nil {
			panic(err)
		}
		org_id = edge_m["InfoFromTerminator"].(map[string]interface{})["OrgID"].(string)
		cur_id = edge_m["InfoFromTerminator"].(map[string]interface{})["ID"].(string)
		msg_ts = edge_m["InfoFromTerminator"].(map[string]interface{})["Timestamp"].(string)
		new_ts = getEpoch(msg_ts)
	}
	edge_msg := &MXEdgeMsg{
		MXEdgeID: cur_id,
		OrgID:    org_id,
		Time:     new_ts,
		AppName:  APP_NAME,
	}
	return edge_msg
}

func processMsg(msg *sarama.ConsumerMessage) {
	edge_msg := extractInfo(msg)
	if edge_msg == nil {
		return
	}
	fmt.Printf("Recv msgs len:%d k:%+v msg:%+v\n", edge_list.Len(), string(msg.Key), edge_msg.Time)
	//new_ts := getEpoch(msg_ts)
	if edge_msg.Time > CURRENT_STREAM_TIME {
		CURRENT_STREAM_TIME = edge_msg.Time
	}
	if edge_ptr, ok := edge_ptr_map[edge_msg.MXEdgeID]; ok {
		// check timestamp on the newly recvd msg and compare it to what
		// we have in the list for this edge. If we recv a more recent msg
		// update the list and put the node at the tail of the list
		// remember, the tail has the edges with the most recent msgs
		cur_ts := edge_ptr.Value.(EdgeState).TimeStamp
		status := edge_ptr.Value.(EdgeState).CadenceStatus
		if cur_ts < edge_msg.Time {
			if status == "DOWN" {
				fmt.Printf("YAY! Edge ID:%s came back to life!\n\n\n", edge_msg.MXEdgeID)
				///////////////////////////////////////////////////////////////////////////
				send_event_msg(producer, "up", edge_msg.MXEdgeID, edge_msg.OrgID, APP_NAME)
				///////////////////////////////////////////////////////////////////////////
			}
			// more recent beat received, lets delete this element and put it
			// at the back of the list
			edge_list.Remove(edge_ptr)
			new_state := EdgeState{
				CadenceStatus: "UP",
				ID:            edge_msg.MXEdgeID,
				OrgID:         edge_msg.OrgID,
				TimeStamp:     edge_msg.Time,
			}
			e := edge_list.PushFront(new_state)
			pos := findPosition(edge_msg.Time)
			if pos != nil {
				edge_list.MoveAfter(e, pos)
			}
			edge_ptr_map[edge_msg.MXEdgeID] = e
			//fmt.Printf("resetting ... %+v", edge_ptr)
		}

	} else {
		// New edge, add in the map and enter a new node in the list
		new_state := EdgeState{
			CadenceStatus: "UP",
			ID:            edge_msg.MXEdgeID,
			OrgID:         edge_msg.OrgID,
			TimeStamp:     edge_msg.Time,
		}
		fmt.Printf("NEW EDGE: %s %+v\n", edge_msg.Time, new_state)
		e := edge_list.PushFront(new_state)
		pos := findPosition(edge_msg.Time)
		if pos != nil {
			edge_list.MoveAfter(e, pos)
		}
		edge_ptr_map[edge_msg.MXEdgeID] = e
		///////////////////////////////////////////////////////////////////////////
		// No need to send a UP event if this is the first time we are hearing
		// about the edge
		// TODO: start checkpointing and then we could send the UP event if we
		// get a new edge. Right now whenever cadence starts, we will end up
		// trigerring UP events for ALL the edges since all of them will be
		// considered new due to no checkpointing.
		//send_event_msg(producer, "up", edge_msg["ID"].(string), edge_msg["OrgID"].(string))
		///////////////////////////////////////////////////////////////////////////
	}
}

func main() {
	edge_list = list.New()
	go sanityChecker()
	edge_ptr_map = make(map[string]*list.Element)
	fmt.Println("Welcome to cadence!")
	fmt.Printf("Broker list:%+v\n", cloud.KAFKA_BROKERS)
	//////////////////////////////////////////////////////////////////
	//setup relevant config info
	configProd := sarama.NewConfig()
	configProd.Producer.Partitioner = sarama.NewRandomPartitioner
	configProd.Producer.Return.Successes = true
	configProd.Producer.RequiredAcks = sarama.WaitForAll
	var err error
	producer, err = sarama.NewSyncProducer(cloud.KAFKA_BROKERS, configProd)
	if err != nil {
		panic(err)
	}
	go checkEdgeStatus(producer)

	//////////////////////////////////////////////////////////////////

	//    st, _ := master.Topics()
	flag.StringVar(&HEART_BEAT_TOPIC, "topic", "marvis-edge-tt-cloud-", "define heart beat topic")
	flag.StringVar(&APP_NAME, "app-name", "mxagent", "define application name")
	flag.Parse()
	HB_TOPIC_FULLNAME = HEART_BEAT_TOPIC + cloud.ENV
	fmt.Printf("Consuming from topic: %s\n", HB_TOPIC_FULLNAME)
	///////////////////////////////////////////////
	///////////////////////////////////////////////
	config := sarama.NewConfig()
	config.Consumer.Offsets.Initial = sarama.OffsetNewest
	version, err := sarama.ParseKafkaVersion("2.1.1")
	config.Version = version
	consumer := Consumer{
		ready: make(chan bool),
	}
	ctx, cancel := context.WithCancel(context.Background())
	client, err := sarama.NewConsumerGroup(cloud.KAFKA_BROKERS, "os-t1", config)
	if err != nil {
		log.Panicf("Error creating consumer group client: %v", err)
	}

	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			if err := client.Consume(ctx, strings.Split(HB_TOPIC_FULLNAME, ","), &consumer); err != nil {
				log.Panicf("Error from consumer: %v", err)
			}
			// check if context was cancelled, signaling that the consumer should stop
			if ctx.Err() != nil {
				return
			}
			consumer.ready = make(chan bool)
		}
	}()

	<-consumer.ready // Await till the consumer has been set up
	log.Println("Sarama consumer up and running!...")
	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-ctx.Done():
		log.Println("terminating: context cancelled")
	case <-sigterm:
		log.Println("terminating: via signal")
	}
	cancel()
	wg.Wait()
	if err = client.Close(); err != nil {
		log.Panicf("Error closing client: %v", err)
	}
	///////////////////////////////////////////////
	///////////////////////////////////////////////
}

func send_event_msg(producer sarama.SyncProducer, op string, id string, org_id string, app_name string) {
	topic := "mxedge-events-staging" //e.g create-user-topic
	msg := &MXEdgeEvent{
		MsgType:  "edge_" + op,
		MXEdgeID: id,
		S3Path:   "NA",
		OrgID:    org_id,
		Time:     time.Now().String(),
		AppName:  app_name,
	}
	message := &sarama.ProducerMessage{
		Topic:     topic,
		Partition: 0,
		Value:     msg,
	}
	partition, offset, _ := producer.SendMessage(message)
	fmt.Printf("Event:%s msg sent %+v %+v ", op, partition, offset)
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
	}(topic, consumer)
	return consumers, errors
}
