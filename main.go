package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"time"

	list "container/list"
	"github.com/Shopify/sarama"
	"github.com/mistsys/cadence/stats"
	"github.com/mistsys/mist_go_utils/cloud"
	"github.com/mistsys/mist_go_utils/flag"
	"github.com/mistsys/mist_go_utils/timeparse"
	"github.com/mistsys/protobuf3/protobuf3"
)

// sorted list for edges, sorting done on ep-term timestamp. In future we can
// make this user defined
var edge_list *list.List

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

// is the topic PB or not
var IS_PB bool

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
		cur_ts := getTimeStamp(e)
		if ts > cur_ts {
			return e
		}
	}
	return e
}

func checkEdgeStatus(producer sarama.SyncProducer) {
	for {
		for e := edge_list.Front(); e != nil; e = e.Next() {
			cur_ts := getTimeStamp(e)
			status := e.Value.(map[string]interface{})["cadence_status"]
			id := e.Value.(map[string]interface{})["ID"]
			org_id := e.Value.(map[string]interface{})["OrgID"]
			fmt.Printf("ID:%s last beat:%d status:%s\n", id, cur_ts, status)
			if CURRENT_STREAM_TIME > 5+cur_ts && status == "UP" {
				fmt.Printf("EDGE is DOWN!!!!\n\n")
				////////////////////////////////////////////////////////////////////
				send_event_msg(producer, "down", id.(string), org_id.(string))
				////////////////////////////////////////////////////////////////////
				e.Value.(map[string]interface{})["cadence_status"] = "DOWN"
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
			cur_ts := getTimeStamp(e)
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

func filterMsg(msg map[string]interface{}) bool {
	// TODO: this function currently has hardcoded stuff
	// this should be made configurable
	return msg["MsgType"] == "sys_metric"
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

func main() {
	edge_list = list.New()
	go sanityChecker()
	edge_ptr_map = make(map[string]*list.Element)
	fmt.Println("Welcome to cadence!")
	fmt.Printf("Broker list:%+v\n", cloud.KAFKA_BROKERS)
	conf := sarama.NewConfig()
	conf.ClientID = "cadence"
	master, err := sarama.NewConsumer(cloud.KAFKA_BROKERS, conf)
	if err != nil {
		panic(err)
	}
	//////////////////////////////////////////////////////////////////
	//setup relevant config info
	config := sarama.NewConfig()
	config.Producer.Partitioner = sarama.NewRandomPartitioner
	config.Producer.Return.Successes = true
	config.Producer.RequiredAcks = sarama.WaitForAll
	producer, err := sarama.NewSyncProducer(cloud.KAFKA_BROKERS, config)
	if err != nil {
		panic(err)
	}
	go checkEdgeStatus(producer)

	//////////////////////////////////////////////////////////////////

	//    st, _ := master.Topics()
	flag.StringVar(&HEART_BEAT_TOPIC, "topic", "marvis-edge-tt-cloud-", "define heart beat topic")
	flag.BoolVar(&IS_PB, "ispb", false, "define if the topic is pb")
	flag.Parse()
	HB_TOPIC_FULLNAME := HEART_BEAT_TOPIC + cloud.ENV
	fmt.Printf("Consuming from topic: %s ispb: %t\n", HB_TOPIC_FULLNAME, IS_PB)
	consumer, errors := consume(HB_TOPIC_FULLNAME, master)
	if errors != nil {
		fmt.Printf("Errors while reading:%+v\n", errors)
	}
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
				var m stats.TTStats
				if err = protobuf3.Unmarshal(msg.Value, &m); err != nil {
					panic(err)
				}
				fmt.Printf("------------------- %+v\n\n\n", m.InfoFromTerminator)
				if !filterMsg(edge_msg) {
					fmt.Printf("Filtering out msg:%s", edge_msg["MsgType"])
					break
				}
				fmt.Printf("Recv msgs len:%d k:%+v msg:%+v\n", edge_list.Len(), string(msg.Key), edge_msg["InfoFromTerminator"].(map[string]interface{})["Timestamp"])
				new_info := edge_msg["InfoFromTerminator"]
				new_ts := getEpoch(new_info.(map[string]interface{})["Timestamp"].(string))
				if new_ts > CURRENT_STREAM_TIME {
					CURRENT_STREAM_TIME = new_ts
				}
				//keys := make([]string, 0, len(edge_msg))
				if edge_ptr, ok := edge_ptr_map[edge_msg["ID"].(string)]; ok {
					// check timestamp on the newly recvd msg and compare it to what
					// we have in the list for this edge. If we recv a more recent msg
					// update the list and put the node at the tail of the list
					// remember, the tail has the edges with the most recent msgs
					//fmt.Printf("found stuff %+v\n\n", edge_ptr.Value.(map[string]interface{})["InfoFromTerminator"].(map[string]interface{})["Timestamp"])
					cur_info := edge_ptr.Value.(map[string]interface{})["InfoFromTerminator"]

					cur_ts := getEpoch(cur_info.(map[string]interface{})["Timestamp"].(string))
					if cur_ts < new_ts {
						status := edge_ptr.Value.(map[string]interface{})["cadence_status"]
						if status == "DOWN" {
							fmt.Printf("YAY! Edge ID:%s came back to life!\n\n\n", edge_msg["ID"])
							///////////////////////////////////////////////////////////////////////////
							send_event_msg(producer, "up", edge_msg["ID"].(string), edge_msg["OrgID"].(string))
							///////////////////////////////////////////////////////////////////////////
						}
						// more recent beat received, lets delete this element and put it
						// at the back of the list
						edge_list.Remove(edge_ptr)
						edge_msg["cadence_status"] = "UP"
						e := edge_list.PushFront(edge_msg)
						pos := findPosition(new_ts)
						if pos != nil {
							edge_list.MoveAfter(e, pos)
						}
						edge_ptr_map[edge_msg["ID"].(string)] = e
						//fmt.Printf("resetting ... %+v", edge_ptr)
					}

				} else {
					// New edge, add in the map and enter a new node in the list
					e := edge_list.PushFront(edge_msg)
					pos := findPosition(new_ts)
					if pos != nil {
						edge_list.MoveAfter(e, pos)
					}
					edge_ptr_map[edge_msg["ID"].(string)] = e
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
				//fmt.Printf("oooooooooooo k:%+v\n\n\n", edge_list.Front())
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

func send_event_msg(producer sarama.SyncProducer, op string, id string, org_id string) {
	topic := "mxedge-events-staging" //e.g create-user-topic
	msg := &MXEdgeEvent{
		MsgType:  "edge_" + op,
		MXEdgeID: id,
		S3Path:   "NA",
		OrgID:    org_id,
		Time:     time.Now().String(),
		AppName:  "mxagent",
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
