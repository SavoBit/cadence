//go:generate easyjson -all -no_std_marshalers structs.go
//go:generate goimports -w structs_easyjson.go
/*
  EP control daemon, EP stats

  Copyright 2014 MistSys
*/

package stats

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mistsys/mist-ap/msgs"
	"github.com/mistsys/mist_go_utils/log"
	"github.com/mistsys/mist_go_utils/net/ethernet"
	"github.com/mistsys/mist_go_utils/net/ip"
	"github.com/mistsys/mist_go_utils/net/wifi"
	"github.com/mistsys/mist_go_utils/uuid"
	"github.com/mistsys/mist_go_utils/wxlan"
	"github.com/mistsys/protobuf3/protobuf3"
)

var dbg = log.Dbg

const (
	AC_BE    = 0 /* Best Effort */
	AC_BK    = 1 /* Background */
	AC_VI    = 2 /* Video */
	AC_VO    = 3 /* Voice */
	AC_COUNT = 4 /* number of ACs */
)

const (
	AW_DISABLED = AW_STATE(0)
	AW_ENROLLED = AW_STATE(1)
	AW_BLOCKED  = AW_STATE(2)
)

type AW_STATE uint16

const MAX_DELAY_RETRY = 7
const MAX_DELAY_HIST_BUCKETS = 16

type APStats struct {
	// in protobuf these fields exist in the TelemetryEnvelope
	Topic    string          `json:",omitempty" protobuf:"-"` // kafka topic to which to publish
	HashKey  string          `json:",omitempty" protobuf:"-"` /// what to hash in order to select a kafka partition to which to publish
	Shuffles []*msgs.Shuffle `json:",omitempty" protobuf:"-"`

	ID     ethernet.MAC `protobuf:"bytes,1"` // the base MAC (id) of the EP sending these stats
	OrgID  uuid.UUID    `protobuf:"bytes,2"` // the organization-id (who owns the device)
	Groups []string     `protobuf:"bytes,3"` // all the groups this EP belongs to
	SiteID uuid.UUID    `protobuf:"bytes,4"` // Site ID configured from the cloud for this AP

	MapID uuid.UUID  `protobuf:"bytes,16"`   // AP location
	XYZ   [3]float64 `protobuf:"fixed64,17"` // relative to the Map's origin

	Uptime          int32   `protobuf:"varint,18"`  // uptime since linux boot, in seconds; varint is ok, it shouldn't be negative
	Model           string  `protobuf:"bytes,19"`   // hardware model ("aph", "apl", ...)
	FirmwareVersion string  `protobuf:"bytes,20"`   // ep firmware version
	CountryCode     string  `protobuf:"bytes,75"`   // country code under which the AP is operating
	Hostname        string  `protobuf:"bytes,70"`   // User friendly AP name. Cisco PACE fills this in so we can present it in the UI.
	Configured      bool    `protobuf:"varint,21"`  // true if the EP has been configured yet (lets the cloud ignore pre-config stats that might not be valid or interesting)
	ConfigTimestamp float64 `protobuf:"fixed64,60"` // current configuration's Timestamp field (config ID+Timestamp uniquely identify the config)
	LicenseExpired  bool    `protobuf:"varint,22"`  // so we can do thing differently (e.g. avoid doing things that costs us) if the license expired
	ClientSummary   bool    `protobuf:"varint,62"`  // legacy (supeceded by full/fast split); true if ClientStats is only a summary (a limited number of stats are filled in)

	When     time.Time `protobuf:"bytes,5"`    // when these stats were gathered
	Interval float64   `protobuf:"fixed64,23"` // the interval between these stats and the previous APStat from this EP, in seconds. this is redundant but convenient for now. 0 the first time an EP sends stats after starting up

	BootCount uint64 `protobuf:"varint,71"` // AP's boot counter. It gets reset when the AP is factory reset, so don't count on it always being accurate

	Proc struct {
		KMsg    []string `protobuf:"bytes,1" json:",omitempty"` // new kernel log lines from /proc/kmsg (really from /dev/kmsg); often empty and thus omittable
		MemInfo struct { // data from /proc/meminfo
			Total_kB     uint64 `protobuf:"varint,1"` // total RAM available to linux in kilobytes
			Available_kB uint64 `protobuf:"varint,2"` // free + buffers + cached in kilobytes (this is rough; it does not take into account locked or dirty cache pages, but it is good enough for a start)
		} `protobuf:"bytes,2"`
		SlabInfo struct { // data from /proc/slabinfo
			Top struct { // top memory using line in /proc/slabinfo (calculated from NumObjs*ObjSize, so freed but still holding onto the RAM counts, but padding to round up to a page doesn't)
				Name       string `protobuf:"bytes,1"`
				ActiveObjs uint32 `protobuf:"varint,2"`
				NumObjs    uint32 `protobuf:"varint,3"`
				ObjSize    uint32 `protobuf:"varint,4"`
			} `protobuf:"bytes,1"`
		} `protobuf:"bytes,3"`
		LoadAvg struct { // data from /proc/loadavg
			Load    [3]float32 `protobuf:"fixed32,1"` // 1/5/15 minute load
			Running uint32     `protobuf:"varint,2"`  // # of runnable tasks
			Total   uint32     `protobuf:"varint,3"`  // # total number of tasks
		} `protobuf:"bytes,4"`
		Raw struct { // raw contents of various memory related files in /proc (included only every EPConfig.Stats.MemInterval for later analysis on the cloud)
			BuddyInfo    string `json:",omitempty" protobuf:"bytes,1"`
			MemInfo      string `json:",omitempty" protobuf:"bytes,2"`
			PageTypeInfo string `json:",omitempty" protobuf:"bytes,3"`
			SlabInfo     string `json:",omitempty" protobuf:"bytes,4"`
			VMStat       string `json:",omitempty" protobuf:"bytes,5"`
			VMAllocInfo  string `json:",omitempty" protobuf:"bytes,6"`
			ZoneInfo     string `json:",omitempty" protobuf:"bytes,7"`
			StatInfo     string `json:",omitempty" protobuf:"bytes,8"`
		} `protobuf:"bytes,5"` // I'd add `json:",omitempty"` but it doesn't work on struct's. They are never considered empty
		FS struct { // information about the filesystems
			RW struct { // info about /rw/ (the only one mounted read-write)
				Total_kB     uint64 `protobuf:"varint,1"`
				Available_kB uint64 `protobuf:"varint,2"`
			} `protobuf:"bytes,1"`
		} `protobuf:"bytes,6"`
		Stat struct { // data from /proc/stat
			TotalCPU CPUStats   `protobuf:"bytes,1"` // total CPU stats from the 'cpu' line in /proc/stat
			CPUs     []CPUStats `protobuf:"bytes,2"` // individual CPU stats from the 'cpu1', 'cpu2', etc lines in /proc/stat
		} `protobuf:"bytes,7"`
	} `protobuf:"bytes,24"`

	Switchports   []SwitchportStats `protobuf:"bytes,25"`
	FDBs          []FDBStats        `protobuf:"bytes,61"`
	VLANs         VLANStats         `protobuf:"bytes,79"`
	SVIs          []SVIStats        `protobuf:"bytes,26"`
	L2TPTunnels   []L2TPTunnelStats `protobuf:"bytes,27"`
	DHCPClients   []DHCPClientStats `protobuf:"bytes,28"`
	IPv4Routes    []IPv4Route       `protobuf:"bytes,29"`
	IPv4Neighbors []IPv4Neighbor    `protobuf:"bytes,73"`
	// protobuf id 30 is reserved
	DNSServers []ip.Addr6 `protobuf:"bytes,50"` // nameservers found in /etc/resolv.conf

	Radios               []RadioStats         `protobuf:"bytes,31"` // copied into WifiRadioScanResults as that will split into its own topic
	RdRoamNotify         []RdRoamNotifyStats  `protobuf:"bytes,77"` // TX/RX roam notify pkt stats
	WLANs                []WLANStats          `protobuf:"bytes,32"`
	Clients              []ClientStats        `protobuf:"bytes,33"`
	Scan                 []ScanResults        `protobuf:"bytes,34"` // LEGACY, use WifiRadioScanResults as that will split into its own topic
	WLANScan             []WLANScanResults    `protobuf:"bytes,78"` // Scanning radio results of WLAN radios on WLAN's channel
	WifiScan             WifiRadioScanResults `protobuf:"bytes,69"`
	BLEScan              []BLEScanResults     `protobuf:"bytes,35"` // LEGACY, use BLEScanResults as that will split into its own topic
	BLEScanResults       []BLEScanResults     `protobuf:"bytes,68"`
	Probes               []ProbeStats         `protobuf:"bytes,36"`
	ClientsRssi          []ClientRssiStats    `protobuf:"bytes,37"` // LEGACY, use BleRssi and WifiRssi
	BleRssi              []BleRssiStats       `protobuf:"bytes,64"` // rssi stats from BLE packets the AP hears
	BleRssiZone          []BleRssiStats       `protobuf:"bytes,66"` // rssi stats from BLE packets the AP hears to be sent to Zone engine
	BleRssiScan          []BLERssiScanStats   `protobuf:"bytes,67"`
	WifiRssi             []WifiRssiStats      `protobuf:"bytes,65"` // rssi stats from probes, mgmt, ack the AP hears (the sender might or might not be associated with the AP)
	WifiRssiZone         []WifiRssiStats      `protobuf:"bytes,74"` // rssi stats from probes, mgmt, ack the AP hears (the sender will not be associated with the AP)
	WifiRogueClients     []WifiRssiStats      `protobuf:"bytes,72"`
	WifiScanChannelStats []WifiChannelStats   `protobuf:"bytes,76"` // wifi scan radio per channel stats: noise floor, cca

	BTRadios []BTRadioStats `protobuf:"bytes,38"`

	MdnsList []MDNS `protobuf:"bytes,39"` // list of all the MDNS advertisements the AP has received

	Airiq        *AiriqStats `protobuf:"bytes,40"`
	AiriqRestart uint32      `protobuf:"varint,41"`

	AppSS            []AppSortServ     `protobuf:"bytes,42"`
	WxlanUsage       []WxlanUsage      `protobuf:"bytes,43"`
	MonitorRssi      []MonitorRssiPkt  `protobuf:"bytes,44"` // DEAD not sent by APs anymore
	ProbeRequestPkts []ProbeRequestPkt `protobuf:"bytes,45"`

	Environment Environment `protobuf:"bytes,80"`

	Cloud CloudStats `protobuf:"bytes,48"`

	Mesh *MeshRemoteStats `protobuf:"bytes,49"`

	// Eth Link layer Neighbor Discovery
	LLDP_nd LLDPneighbor `protobuf:"bytes,56"`

	// Eth Link Layer Power Negotiation stats.
	LLDP_pwr LLDPpwrStats `protobuf:"bytes,57"`

	FlowState []WxlanFlowState `protobuf:"bytes,59"` // pass up flows we blocked or in pasued when RestPUT timedout

	ExtIO []ExtIOStats `protobuf:"bytes,58" json:",omitempty"` // optional; absent on APs without an external IO connector

	PMGR_pwr PMGRStats `protobuf:"bytes,63"` // Power manager Stats about power source.

	// below are fields added by the terminator
	InfoFromTerminator *msgs.InfoFromTerminator `protobuf:"bytes,2047" json:",omitempty"` // data about the message inserted by the terminator. This is more trustworthy than the inner contents of the message because the EP can't control the contents. NOTE ID 2047 matches msgs.InfoFromTerminatorID value
}

type HostPort struct {
	Host string `protobuf:"bytes,1"`
	Port uint16 `protobuf:"varint,2"`
}

func (hp HostPort) String() string {
	return fmt.Sprintf("Host:%v Port:%v", hp.Host, hp.Port)
}

// one MDNS advertisement
type MDNS struct {
	// is it necessary to split up IPv4 & IPv6?
	Mac  ethernet.MAC `protobuf:"bytes,1"`
	Vlan uint16       `protobuf:"varint,2"`
	// protobuf id 3 is reserved
	A      []ip.Addr4 `protobuf:"bytes,8"`
	AAAA   []ip.Addr6 `protobuf:"bytes,4"`
	Ptr    []string   `protobuf:"bytes,5"` // any value to send the collection of alias?
	Txt    []string   `protobuf:"bytes,6"`
	Target []HostPort `protobuf:"bytes,7"`
}

// information concerning an SVI (switch vlan interface)
type SVIStats struct {
	Dev  string         `protobuf:"bytes,1"`  // net_device name ("vlan1")
	Vlan uint16         `protobuf:"varint,2"` // numeric vlan id (1)
	IPs  []ip.AddrMask6 `protobuf:"bytes,3"`  // note: can be empty, can contain both IPv4 and IPv6, and can have more than one element
}

// one IPv4 route
type IPv4Route struct {
	Dst ip.Subnet4 `protobuf:"bytes,1"` // in the case of the default gateway the Dst is 0.0.0.0/0
	// protobuf id 2 is reserved
	Gw ip.Addr4 `protobuf:"bytes,3"` // in the case of local subnets the Gw is empty (since there isn't one)
}

// one IPv4 neighbor (ARP) entry
type IPv4Neighbor struct {
	IP  ip.Addr4     `protobuf:"bytes,1"`
	MAC ethernet.MAC `protobuf:"bytes,2"`
}

// information concerning one L2TP tunnel
// NOTE WELL there can be duplicate ID values with different LocalConnectionID in the same stats structure during tunnel reconfiguration
type L2TPTunnelStats struct {
	ID                    uuid.UUID          `protobuf:"bytes,1"`   // PAPI ID of tunnel
	State                 string             `protobuf:"bytes,2"`   // tunnel state machine state
	LocalAddr             ip.AddrPort6       `protobuf:"bytes,22"`  // local IP and port
	PeerAddr              ip.AddrPort6       `protobuf:"bytes,23"`  // peer IP and port
	RouterID              uint32             `protobuf:"fixed32,4"` // host-endian; configured Router-ID
	Uptime                uint32             `protobuf:"varint,5"`  // seconds since tunnel and session(s) (pseudowires) were all established
	LocalConnectionID     uint32             `protobuf:"fixed32,6"` // host-endian; dynamic tunnel connection id
	RemoteConnectionID    uint32             `protobuf:"fixed32,7"` // host-endian; dynamic tunnel connection id of peer
	TxControlPkts         uint32             `protobuf:"varint,8"`  // count of control packets sent
	RxControlPkts         uint32             `protobuf:"varint,9"`  // count of control packets received
	Sessions              []L2TPSessionStats `protobuf:"bytes,10"`
	OuterPMTU             uint16             `protobuf:"varint,11"` // outer Path-MTU, or 0 if we don't know
	LastStopCCNResult     L2TPResultCode     `protobuf:"bytes,12"`  // RESULT_CODE AVP value from last StopCCN we sent
	LastPeerStopCCNResult L2TPResultCode     `protobuf:"bytes,13"`  // RESULT_CODE AVP value from last StopCCN we received
	PeerMistID            uuid.UUID          `protobuf:"bytes,14"`  // MIST_ID AVP value from last SCCRQ or SCCRP we received
	PeerMistOrgID         uuid.UUID          `protobuf:"bytes,15"`  // MIST_ORG AVP value from last SCCRQ or SCCRP we received
	PeerMistSiteID        uuid.UUID          `protobuf:"bytes,16"`  // MIST_SITE AVP value from last SCCRQ or SCCRP we received

	// obsolete fields. Newer firmware don't send these fields
	ObsoletePeer string `json:",omitempty" protobuf:"bytes,3"` // replaced by PeerAddr in drogo and newer firmware; IP address:port# for UDP tunnels, or just IP address for IP tunnels
}

// the value of an L2TP RESULT_CODE AVP
type L2TPResultCode struct {
	Code    uint16    `protobuf:"varint,1"`
	ErrCode uint16    `protobuf:"varint,2"`
	ErrMsg  string    `protobuf:"bytes,3"`
	When    time.Time `protobuf:"bytes,4"`
}

func (rc L2TPResultCode) IsZero() bool {
	return rc.Code == 0 && rc.ErrCode == 0 && rc.ErrMsg == ""
}

func (rc L2TPResultCode) String() string {
	if rc.IsZero() {
		return ""
	}
	if rc.When.IsZero() {
		return fmt.Sprintf("Result Code %d, Error Code %d, Error Msg %q", rc.Code, rc.ErrCode, rc.ErrMsg)
	}
	return fmt.Sprintf("Result Code %d, Error Code %d, Error Msg %q at %v", rc.Code, rc.ErrCode, rc.ErrMsg, rc.When.Format("2006-01-02T15:04:05.999Z07:00"))
}

// information concerning one L2TP session
type L2TPSessionStats struct {
	Dev             string `protobuf:"bytes,1"`   // net_device name(s) ("l2tp0"). Doesn't mean anything except to developers since sessions are dynamically established and take on the next available name
	RemoteEndID     string `protobuf:"bytes,2"`   // configured Remote-End-ID
	State           string `protobuf:"bytes,3"`   // session state machine state
	LocalSessionID  uint32 `protobuf:"fixed32,4"` // host-endian; dynamic session id
	RemoteSessionID uint32 `protobuf:"fixed32,5"` // host-endian; dynamic session id of peer
}

// information on the AP's DHCP clients
type DHCPClientStats struct {
	Dev   string `protobuf:"bytes,1"` // net_device name ("vlan1")
	State string `protobuf:"bytes,2"` // DHCP client state

	RenewedAt time.Time `protobuf:"bytes,3"`  // timestamp when the lease was last renewed
	LeaseTime uint32    `protobuf:"varint,4"` // time length of the lease (total) in seconds
	// protobuf id 5 is reserved
	Server ip.Addr4 `protobuf:"bytes,10"` // DHCP server which offered the lease, or 0.0.0.0

	IP ip.AddrMask4 `protobuf:"bytes,6"` // the leased IP, or 0.0.0.0/24 if nothing is leased
	// protobuf ids 7 and 8 are reserved
	Gateway  ip.Addr4   `protobuf:"bytes,11"`                   // the default gateway (or 0.0.0.0)
	DNS      []ip.Addr4 `json:",omitempty" protobuf:"bytes,12"` // DNS server(s) from in the lease (can be empty)
	Domain   string     `json:",omitempty" protobuf:"bytes,13"` // domain name from lease (can be empty)
	NTP      []ip.Addr4 `json:",omitempty" protobuf:"bytes,14"` // NTP server(s) from the lease (can be empty)
	ProxyURL string     `json:",omitempty" protobuf:"bytes,15"` // http proxy from lease (can be empty)

	Err string `json:",omitempty" protobuf:"bytes,9"` // if State=="ERROR", the error in question
}

type SwitchportStats struct {
	Name       string `protobuf:"bytes,1"`  // name of switchport (eth0)
	Link       bool   `protobuf:"varint,2"` // whether the PHY has link
	FullDuplex bool   `protobuf:"varint,3"` // full duplex
	//AutoNeg    bool  `protobuf:"varint,4"` // whether it's auto negotiated (commented out b/c for the moment we always autonegociate)
	PoweredDown bool   `protobuf:"varint,10"` // true if the PHY is in powerdown state
	Mbps        uint32 `protobuf:"varint,5"`  // link speed in units of Mbps (10, 100, 1000, 2500)

	RxPeakbps uint64 `protobuf:"varint,27"` // peak bits/sec at the MAC (not the PHY, which adds overhead in terms of preambles and inter-frame-gaps and such) over some reasonable but small time interval no less than 0.1 seconds, and probably closer to 2 seconds
	RxBytes   uint64 `protobuf:"varint,6"`  // rx bytes
	RxPkts    uint32 `protobuf:"varint,7"`  // rx packets

	RxPausePkts       uint32 `json:",omitempty" protobuf:"varint,16"` // rx 802.3x pause frames (not an error, just a statement of fact)
	RxErrors          uint32 `json:",omitempty" protobuf:"varint,17"` // rx packets with sort of error (the sum of the below fields + some other errors we don't break out)
	RxUndersizeErrors uint32 `json:",omitempty" protobuf:"varint,18"` // < 64 bytes but good checksum
	RxOversizeErrors  uint32 `json:",omitempty" protobuf:"varint,19"` // > approx 1518 bytes but good checksum
	RxJabberErrors    uint32 `json:",omitempty" protobuf:"varint,20"` // > approx 1518 bytes with bad checksum or alignment
	RxAlignmentErrors uint32 `json:",omitempty" protobuf:"varint,21"` // not a multiple of 8 bits long
	RxFCSErrors       uint32 `json:",omitempty" protobuf:"varint,22"` // checksum incorrect
	RxFragmentErrors  uint32 `json:",omitempty" protobuf:"varint,23"` // < 64 bytes with bad checksum or alignment
	RxSymbolErrors    uint32 `json:",omitempty" protobuf:"varint,24"` // invalid data symbol(s)
	RxOverrunErrors   uint32 `json:",omitempty" protobuf:"varint,25"` // unsufficient rx buffers (can't keep up)

	RxDiscards uint32 `json:",omitempty" protobuf:"varint,26"` // # of pkts discarded by forwarding rules, rate limits, ACLs, etc... . (not an error)

	TxPeakbps uint64 `protobuf:"varint,28"` // peak bits/sec at the MAC (not the PHY, which adds overhead in terms of preambles and inter-frame-gaps and such) over some reasonable but small time interval no less than 0.1 seconds, and probably closer to 2 seconds
	TxBytes   uint64 `protobuf:"varint,8"`  // tx bytes
	TxPkts    uint32 `protobuf:"varint,9"`  // tx packets

	TxCollisions uint32 `json:",omitempty" protobuf:"varint,29"` // half-duplex collisions during [successfull or not] transmission (not an error)

	TxErrors                   uint32 `json:",omitempty" protobuf:"varint,30"` // tx packets with sort of error (the sum of the below fields + some other errors we don't break out)
	TxExcessiveCollisionErrors uint32 `json:",omitempty" protobuf:"varint,31"` // # of pkts dropped because they had 16 tx collisions in a row
	TxUnderrunErrors           uint32 `json:",omitempty" protobuf:"varint,32"` // insufficient tx DMA (can't keep up)
}

// information from the FDBs in the AP
type FDBStats struct {
	Name    string          `protobuf:"bytes,1"`                   // name of fdb ("wxlan", "robo")
	Count   int             `protobuf:"varint,2"`                  // total # of entries
	Entries []FDBEntryStats `json:",omitempty" protobuf:"bytes,3"` // selected FDB entries (depends on configuration)
}

type FDBEntryStats struct {
	MAC  ethernet.MAC `protobuf:"bytes,1"`
	Vlan uint16       `protobuf:"varint,2"`
	Port uint16       `protobuf:"varint,3"` // port numbering depends on the hardware model and firmware version. low numbers correspond to external switchports
}

type VLANStats struct {
	InactiveWiredVLANs []uint16 `protobuf:"varint,1"` // VLANs which are configured on the wired ports but no packets have been received on this VLAN since the previous full stat
}

type TxDelayStats struct {
	Hist [MAX_DELAY_HIST_BUCKETS]uint32 `protobuf:"varint,1"` // Delay (latency) histogram, bucket resoultion is one millisec
	// The latency is measured from radio fifo enqueue to ACK recv'd
	// Note, power save latency (client is ps mode and wake for DTIM) in included
	RetryHist [MAX_DELAY_RETRY]uint32 `protobuf:"varint,2"` // Retry histogram (all AMPDU are recorded in first retry bucket)
	RetrySum  [MAX_DELAY_RETRY]uint32 `protobuf:"varint,3"` // cumulative delay (latency) in millisec per retry attempts
	NoAck     uint32                  `protobuf:"varint,4"` // number of packets no ACK
	Min       uint32                  `protobuf:"varint,5"` // minimum packet latency observed
	Max       uint32                  `protobuf:"varint,6"` // maximum packet latency observed
}

type AmpduTxCounter struct { //Ampdu TX counters
	// Ampdu Initiator (TXing)
	FifoFull uint32 `protobuf:"varint,1"` // release TX ampdu due to insufficient tx descriptors
	Drop     uint32 `protobuf:"varint,2"` // Tx Dropped Pkts
	Stuck    uint32 `protobuf:"varint,3"` // watchdog bailout for stuck state
	Orphan   uint32 `protobuf:"varint,4"` // orphan pkts where scb/ini has been cleaned
	StuckPS  uint32 `protobuf:"varint,5"` // watchdog bailout for stuck state WC in Power Save Mode

	R0Hole uint32 `protobuf:"varint,6"` // Lost packet between AP and Client
	RNHole uint32 `protobuf:"varint,7"` // Lost retried packet
	RLag   uint32 `protobuf:"varint,8"` // Laggard packet (lost)

	TxAddBaReq  uint32 `protobuf:"varint,9"`  // Send Add Block Request, at start and on addba timeouts
	RxAddBaResp uint32 `protobuf:"varint,10"` // Recd Add Block Response, recd addba response
	Lost        uint32 `protobuf:"varint,11"` // Lost packets, reported by ucode in pkt tx status
	TxBar       uint32 `protobuf:"varint,12"` // Send Block Ack Request
	RxBa        uint32 `protobuf:"varint,13"` // Recd Block Ack Response
	NoBa        uint32 `protobuf:"varint,14"` // Block Ack missing

	RxUnexpect uint32 `protobuf:"varint,15"` // Recd unexpected packets from client (like action frame, addba resp)
	TxDelBa    uint32 `protobuf:"varint,16"` // send Delete Block Ack
	RxDelBa    uint32 `protobuf:"varint,17"` // recd Delete Block Ack

	BlockDataFifo    uint32 `protobuf:"varint,18"` // Tx Block Data Fifo
	OrphanBadIni     uint32 `protobuf:"varint,19"` // orphan due to ucode issues
	DropNoBuf        uint32 `protobuf:"varint,20"` // Tx Dropped Pkts, due to Tx Queue Overflow
	DropErr          uint32 `protobuf:"varint,21"` // Tx Dropped Pkts, due to error in the packet
	OrphanBadIniFree uint32 `protobuf:"varint,22"` // orphan due to ucode issues, free the pkt

	TxAmpduSucc uint32 `protobuf:"varint,23"` // roll up of all scb TxAmpduSucc
}

type AmpduRxCounter struct { //Ampdu RX counters
	// Ampdu RXing from client (the Ampdu initiator)
	Holes       uint32 `protobuf:"varint,1"`  // missed seq numbers on rx side
	Stuck       uint32 `protobuf:"varint,2"`  // watchdog bailout for stuck state
	RxAddBaReq  uint32 `protobuf:"varint,3"`  // Recd Add Block Request (addba resp) from Ampdu Initiator (client)
	TxAddBaResp uint32 `protobuf:"varint,4"`  // Send Add Block Respons (addba response) to Ampdu Initiator (client)
	RxBar       uint32 `protobuf:"varint,5"`  //  Block Ack Request Recd from Ampdu Initiator (client)
	TxBa        uint32 `protobuf:"varint,6"`  // Block Ack Recd from Ampdu Initiator (client)
	RxUnexpect  uint32 `protobuf:"varint,7"`  // Recd unexpected packets from client
	TxDelBa     uint32 `protobuf:"varint,8"`  // Send Delete Block Ack to Ampdu Initiator (client)
	RxDelBa     uint32 `protobuf:"varint,9"`  // Recd Delete Block Ack from Ampdu Initiator (client)
	RxMpdu      uint32 `protobuf:"varint,10"` // mpdus recd in a ampdu
	RxAmpdu     uint32 `protobuf:"varint,11"` // ampdu recd
}

type NarStats struct { // Non Aggregated Regulation stats
	Queued   uint32 `protobuf:"varint,1"` // #packets queued in NAR
	Dequeued uint32 `protobuf:"varint,2"` // #packets dequeded in NAR
	Held     uint32 `protobuf:"varint,3"` // #packets held/regulated due to in transit limit
	Dropped  uint32 `protobuf:"varint,4"` // #packets dropped, queue full
}

type PktQueueStats struct { //Packet Queue Stats
	Requested   uint32 `protobuf:"varint,1"` // number of packets Requested to be stored in Tx queue pktq_stats,
	FullDropped uint32 `protobuf:"varint,2"` // number of packets dropped because tx queue full
	Dropped     uint32 `protobuf:"varint,3"` // number of packets dropped in tx queue
	Retried     uint32 `protobuf:"varint,4"` // packets re-sent because they are not received
	Rtsfail     uint32 `protobuf:"varint,5"` // count of rts attempts that failed to receive cts
	Throughput  uint32 `protobuf:"varint,6"` // actual data transferred successfully
	Airuse      uint32 `protobuf:"varint,7"` // airuse
	Max_used    uint32 `protobuf:"varint,8"` // the high-water mark of the queue utilisation for packets
	Length      uint32 `protobuf:"varint,9"` // the maximum capacity fo the queue
}

type PktPendStats struct { //Packet Pending Stats per type and module
	BK     uint32 `protobuf:"varint,1"`  // number of packets pending in BK queue
	BE     uint32 `protobuf:"varint,2"`  // number of packets pending in BE queue
	VI     uint32 `protobuf:"varint,3"`  // number of packets pending in VI queue
	VO     uint32 `protobuf:"varint,4"`  // number of packets pending in VO queue
	BCMC   uint32 `protobuf:"varint,5"`  // number of packets pending in VO queue
	ATIM   uint32 `protobuf:"varint,6"`  // number of packets pending in ATIM queue
	Common uint32 `protobuf:"varint,7"`  // number of packets pending in common module
	Ampdu  uint32 `protobuf:"varint,8"`  // number of packets pending in AMPDU module
	Amsdu  uint32 `protobuf:"varint,9"`  // number of packets pending in AMSDU module
	PS     uint32 `protobuf:"varint,10"` // number of packets pending in Power Save (apps) module
	HW     uint32 `protobuf:"varint,11"` // number of packets pending driver in HW queues
	All    uint32 `protobuf:"varint,12"` // number of packets pending in all modules and HW queues
}

type WCQoSStats struct { // Tx and Rx Pkts per QoS type
	TxPkts  uint32 `protobuf:"varint,1"`
	TxBytes uint32 `protobuf:"varint,2"`
	RxPkts  uint32 `protobuf:"varint,3"`
	RxBytes uint32 `protobuf:"varint,4"`
}

type QoSStats struct { // Tx and Rx Pkts per QoS type
	TxPkts        uint32 `protobuf:"varint,1"`
	TxBytes       uint32 `protobuf:"varint,2"`
	TxFailedPkts  uint32 `protobuf:"varint,3"`
	TxFailedBytes uint32 `protobuf:"varint,4"`

	RxPkts        uint32 `protobuf:"varint,5"`
	RxBytes       uint32 `protobuf:"varint,6"`
	RxFailedPkts  uint32 `protobuf:"varint,7"`
	RxFailedBytes uint32 `protobuf:"varint,8"`
}

// per associated wifi client statistics
// NOTE WELL: in a single AP the SAME client can appear several times, on different radios and/or or different WLANS.
// (This is no different than the same client appearing on different APs)
// This happens because the races and timeouts when the client roams from one radio to another or one WLAN to another.
// Never assume ClientStats.MAC is a unique key within a single AP's stats. Use {MAC,WlanID,RadioIndex} or {MAC,BSSID} if you must have uniqueness.
type ClientStats struct {
	// these first 3 fields form a triplet which uniquely determines this ClientStats on this AP
	MAC        ethernet.MAC `protobuf:"bytes,1"`  // the client's MAC address (at least the one the client is using to associate with this AP; some clients vary their MACs between associations)
	RadioIndex uint8        `protobuf:"varint,2"` // index into the RadioStats array of the radio with which the client is associated
	Band       wifi.Band    `protobuf:"varint,3"` // wifi band (2.4, 5) the client is assocated on
	BSSID      ethernet.MAC `protobuf:"bytes,4"`  // BSSID with which the client is associated
	WLAN       struct {
		ID   uuid.UUID `protobuf:"bytes,1"` // id of wlan the client is associated with (unambiguous; for computers)
		SSID string    `protobuf:"bytes,2"` // SSID of wlan the client is associated with (ambiguous; for humans)
	} `protobuf:"bytes,5"`

	ConnectedTimeSec     uint32      `protobuf:"varint,6"`    // note this is in seconds, while the inactive time is in millisec
	InactiveTimeMilliSec uint32      `protobuf:"varint,7"`    // time since the last transmission from the client
	RSSI                 wifi.RSSI   `protobuf:"zigzag32,8"`  // of last data transmission from the client (averaged from PerAntennaRSSI)
	PerAntennaRSSI       []wifi.RSSI `protobuf:"zigzag32,9"`  // raw, per-antenna RSSIS (averaged into ClientStats.RSSI)
	AvgRSSI              wifi.RSSI   `protobuf:"zigzag32,10"` // averaged over some number of transmissions, an average of ClientStats.RSSI

	TxBitRate      wifi.Rate `protobuf:"varint,11"` // rate used by last successfull (acked) tx in units of 100kbits/sec
	TxUnicastBytes uint64    `protobuf:"varint,12"` // bytes sent directly to the WC (including the results of mc->uc conversions)
	TxUnicastPkts  uint32    `protobuf:"varint,13"` // pkts sent directly to the WC (including the results of mc->uc conversions) - pkts from the wl interface
	// NOTE: there are no Tx stats for broadcast and multicast to a WC. In wifi b/mcast are sent to all clients on the BSSID at once
	// so the b/mcast stats are per-BSSID
	TxPktsSent       uint32 `protobuf:"varint,14"` // # user frames sent successfully - tx status stats
	TxRetries        uint32 `protobuf:"varint,15"` // how many times a packet was sent with the retry bit set (so 3 tx attempts increments this by 2)
	TxRetried        uint32 `protobuf:"varint,16"` // how many packets were retried once or more (so 3 tx attempts only increments this by 1)
	TxFailed         uint32 `protobuf:"varint,17"` // total TX packet no ack or txphy error
	Txbps            uint32 `protobuf:"varint,18"` // TxBytes * 8 / <interval between now and the previous ClientStats message>: naturally somewhat meaningless in the first message after a client associates
	Txpps            uint32 `protobuf:"varint,19"` // TxUnicastPkts / <interval between now and the previous ClientStats message>: naturally somewhat meaningless in the first message after a client associates
	TxRetryExhausted uint32 `protobuf:"varint,20"` // number of user frames where a retry was exhausted
	TxRateFallBack   uint32 `protobuf:"varint,21"` // lowest fallback TX rate

	RxBitRate         wifi.Rate `protobuf:"varint,22"` // rate of last rx in units of 100kbits/sec
	RxBytes           uint64    `protobuf:"varint,23"` // total bytes, including RxMcastBytes
	RxPkts            uint32    `protobuf:"varint,24"` // total packets, including RxMcastPkts
	RxMcastPkts       uint32    `protobuf:"varint,25"` // multicast & broadcast both
	RxMcastBytes      uint64    `protobuf:"varint,26"` // multicast & broadcast both
	RxRetried         uint32    `protobuf:"varint,27"` // number of packets successfully received with the 802.11 retry flag set
	RxRetries         uint32    `protobuf:"varint,28"` // number of packets successfully received with the 802.11 retry flag set
	RxDups            uint32    `protobuf:"varint,29"` // number of duplicate packets received (presumably included in RxRetries, since any duplicates should have the 802.11 retry flag set, but hey, clients can have bugs...)
	RxDecryptFailures uint32    `protobuf:"varint,30"` // number of packets we couldn't decrypt successfully
	Rxbps             uint32    `protobuf:"varint,31"` // RxBytes * 8 / <interval between now and the previous ClientStats message>: naturally somewhat meaningless in the first message after a client associates
	Rxpps             uint32    `protobuf:"varint,32"` // RxPkts / <interval between now and the previous ClientStats message>: naturally somewhat meaningless in the first message after a client associates
	RxAmpdu           uint32    `protobuf:"varint,86"` // mpdus recd in an ampdu

	RxProbeReqs24 uint32 `protobuf:"varint,33"` // Rx Probe Request in the 2.4GHz Band, heard by WLAN radio
	RxProbeReqs5  uint32 `protobuf:"varint,34"` // Rx Probe Request in the 5GHz Band, heard by WLAN radio

	PqRequested   uint32 `protobuf:"varint,35"` // number of packets Requested to be stored in Tx queue pktq_stats,
	PqFullDropped uint32 `protobuf:"varint,81"` // number of packets dropped  in tx queue
	PqDropped     uint32 `protobuf:"varint,36"` // number of packets dropped  in tx queue
	PqRetried     uint32 `protobuf:"varint,37"` // packets re-sent because they are not received
	PqRtsfail     uint32 `protobuf:"varint,38"` // count of rts attempts that failed to receive cts
	PqThroughput  uint32 `protobuf:"varint,39"` // actual data transferred successfully
	PqAiruse      uint32 `protobuf:"varint,40"` // airuse
	PqMax_used    uint32 `protobuf:"varint,41"` // the high-water mark of the queue utilisation for packets
	PqLength      uint32 `protobuf:"varint,42"` // the maximum capacity fo the queue

	PSPq PktQueueStats `protobuf:"bytes,82"` // Power Save Packet Queue Stats

	// Delay (latency) stats are per AC queue
	TxDelay   [AC_COUNT]TxDelayStats `protobuf:"bytes,43"`
	Hostname  string                 `protobuf:"bytes,44"`
	UserAgent string                 `protobuf:"bytes,45"`
	// protobuf id 46 is reserved
	Ipv4     ip.Addr4 `protobuf:"bytes,55"`
	VlanID   uint16   `protobuf:"varint,47"`
	Username string   `protobuf:"bytes,48"`

	// Rate Stats
	CCKRates  CCKRateStats  `protobuf:"bytes,49"`
	OFDMRates OFDMRateStats `protobuf:"bytes,50"`
	MCSRates  MCSRateStats  `protobuf:"bytes,51"`

	// Capabilities
	Protocol   string `protobuf:"bytes,52"` // 802.11 protocol type ("a", "b", bg", "n", "ac")
	NumStreams uint8  `protobuf:"varint,53"`

	MultiPSKName string `protobuf:"bytes,54"` // this field is obsolete. Older FW still generate this but no cloud support for it.

	// WCID uuid.UUID `protobuf:"bytes,56"` // obfuscated MAC

	MultiPSKID uuid.UUID `protobuf:"bytes,57" json:",omitempty"` // the uuid associated with the PPSK this client is using

	// Steering Stats
	DualBand bool `protobuf:"varint,58"` // true if AP determines client supports dual band (2.4GHz and 5GHz)

	UnConnectedTimeSec  uint32 `protobuf:"varint,59"` // time in seconds since first heard unconnected client
	RxUnconnectedPkts24 uint32 `protobuf:"varint,60"` // Rx Pkts Unconnected Wifi 2.4GHz Band (heard by scanning radio)
	RxUnconnectedPkts5  uint32 `protobuf:"varint,61"` // Rx Pkts Unconnected Wifi 5GHz Band (heard by scanning radio)

	NoProbeResp24 uint32 `protobuf:"varint,62"` // no. of times did not send probe response in 2.4GHz band
	NoProbeResp5  uint32 `protobuf:"varint,63"` // no. of times did not send probe response in 5GHz band

	SteerDenyAuth24 uint32 `protobuf:"varint,64"` // no. of times deny authentication in 2.4GHz Band
	SteerDenyAuth5  uint32 `protobuf:"varint,65"` // no. of times deny authentication in 5GHz Band

	AKM      string `protobuf:"bytes,66"` // authentication type. "WPA2-PSK", "WPA-TKIP" etc..., or empty
	Pairwise string `protobuf:"bytes,67"` // pairwise crypto ("CCMP", "TKIP" or empty)

	//Ampdu AmpduCounter `protobuf:"bytes,68"` // Ampdu TX/RX counters
	AmpduTx AmpduTxCounter `protobuf:"bytes,69"` // Ampdu TX counters
	AmpduRx AmpduRxCounter `protobuf:"bytes,70"` // Ampdu RX counters

	//WhenLastDeltaStats time.Time `protobuf:"bytes,71"` // when previous stats were gather for delta stats calculation
	RadIfname string `protobuf:"bytes,72"` // radius airspace attribute for dyname vlan

	AirWatchState AW_STATE `protobuf:"varint,73" json:",omitempty"` // disabled, enrolled, blocked

	// additional Tx and Rx stats
	TxBCMC2Unicast      uint32 `protobuf:"varint,74"` // Tx BCMC2Unicast packets at interface before Tx status
	TxFailedArp         uint32 `protobuf:"varint,75"` // TX Arp failures - txphy error or no ACK
	TxDropArp           uint32 `protobuf:"varint,76"` // TX Arp dropped - in ampdu
	TxBCMC2UnicastBytes uint64 `protobuf:"varint,77"` // Tx BCMC2Unicast bytes at interface before Tx status

	// Power Save stats
	PwrSave PwrSaveStats `protobuf:"bytes,78"`

	// NAR - Non Aggregated Regulation
	NonAggrRegulate NarStats             `protobuf:"bytes,79"`
	Bandwidth       wifi.Width           `protobuf:"varint,80"` // max bandwidth (20/40/80/160) supported by client in this association
	LastStatsSecAgo int                  `protobuf:"varint,83"` // non-zero for clients who are no longer associated, secs since last stats recorded
	Group           string               `protobuf:"bytes,84"`  // radius ACL role.  Could be Airespace_ACL_name or Aruba_User_Role
	QoS             [AC_COUNT]WCQoSStats `protobuf:"bytes,85"`  // Tx and Rx WME Counters per QOS type

	Copied             *ClientStatsCopied       `protobuf:"bytes,2016" json:",omitempty"`
	InfoFromTerminator *msgs.InfoFromTerminator `protobuf:"bytes,2047" json:",omitempty"`
}

// the ep-telemetry shuffle's copied values in a client stats msg. filled in by ep-terminator
type ClientStatsCopied struct {
	ID     ethernet.MAC `protobuf:"bytes,1"` // the base MAC (id) of the EP sending these stats
	OrgID  uuid.UUID    `protobuf:"bytes,2"` // the organization-id (who owns the device)
	SiteID uuid.UUID    `protobuf:"bytes,4"` // Site ID configured from the cloud for this AP

	MapID uuid.UUID  `protobuf:"bytes,16"`   // AP location
	XYZ   [3]float64 `protobuf:"fixed64,17"` // relative to the Map's origin

	When time.Time `protobuf:"bytes,5"` // when these stats were gathered
}

type CCKRateStats struct {
	Rx1Mbps      uint32 `protobuf:"varint,1"`
	Tx1Mbps      uint32 `protobuf:"varint,2"`
	Tx1MbpsSucc  uint32 `protobuf:"varint,3"`
	Rx2Mbps      uint32 `protobuf:"varint,4"`
	Tx2Mbps      uint32 `protobuf:"varint,5"`
	Tx2MbpsSucc  uint32 `protobuf:"varint,6"`
	Rx5Mbps      uint32 `protobuf:"varint,7"`
	Tx5Mbps      uint32 `protobuf:"varint,8"`
	Tx5MbpsSucc  uint32 `protobuf:"varint,9"`
	Rx11Mbps     uint32 `protobuf:"varint,10"`
	Tx11Mbps     uint32 `protobuf:"varint,11"`
	Tx11MbpsSucc uint32 `protobuf:"varint,12"`
}

type OFDMRateStats struct {
	Rx6Mbps      uint32 `protobuf:"varint,1"`
	Tx6Mbps      uint32 `protobuf:"varint,2"`
	Tx6MbpsSucc  uint32 `protobuf:"varint,3"`
	Rx9Mbps      uint32 `protobuf:"varint,4"`
	Tx9Mbps      uint32 `protobuf:"varint,5"`
	Tx9MbpsSucc  uint32 `protobuf:"varint,6"`
	Rx12Mbps     uint32 `protobuf:"varint,7"`
	Tx12Mbps     uint32 `protobuf:"varint,8"`
	Tx12MbpsSucc uint32 `protobuf:"varint,9"`
	Rx18Mbps     uint32 `protobuf:"varint,10"`
	Tx18Mbps     uint32 `protobuf:"varint,11"`
	Tx18MbpsSucc uint32 `protobuf:"varint,12"`
	Rx24Mbps     uint32 `protobuf:"varint,13"`
	Tx24Mbps     uint32 `protobuf:"varint,14"`
	Tx24MbpsSucc uint32 `protobuf:"varint,15"`
	Rx36Mbps     uint32 `protobuf:"varint,16"`
	Tx36Mbps     uint32 `protobuf:"varint,17"`
	Tx36MbpsSucc uint32 `protobuf:"varint,18"`
	Rx48Mbps     uint32 `protobuf:"varint,19"`
	Tx48Mbps     uint32 `protobuf:"varint,20"`
	Tx48MbpsSucc uint32 `protobuf:"varint,21"`
	Rx54Mbps     uint32 `protobuf:"varint,22"`
	Tx54Mbps     uint32 `protobuf:"varint,23"`
	Tx54MbpsSucc uint32 `protobuf:"varint,24"`
}

type MCSRateStats struct {
	MCSType string `protobuf:"bytes,1"` // "HT" (.11n), "VHT" (.11ac)

	// stats for non-AMPDU frames
	Rx     MCSRateCounters `protobuf:"bytes,2"` // index as [stream][mcs];only contains as many streams and MCS rates as the client can use
	Tx     MCSRateCounters `protobuf:"bytes,3"` // total tx'ed (successfull or not)
	TxSucc MCSRateCounters `protobuf:"bytes,4"` // total successfully tx'ed (ACK received)

	// stats for AMPDU data frames
	RxAMPDU     MCSRateCounters `protobuf:"bytes,5"` // index as [stream][mcs]; only contains as many streams and MCS rates as the client can use
	TxAMPDU     MCSRateCounters `protobuf:"bytes,6"` // total tx'ed (successfull or not)
	TxAMPDUSucc MCSRateCounters `protobuf:"bytes,7"` // total successfully tx'ed (ACK received). What exactly that means for an AMPDU I don't know, since a Block-ACK can ack some but not all of the AMPDU

	// delta stats for AMPDU data frames
	// [stream, mcs rate, delta counter]
	//RxAMPDUDelta     MCSRateCounters `protobuf:"bytes,8"`  // list of non-zero delta counters from RxAMPDU
	//TxAMPDUDelta     MCSRateCounters `protobuf:"bytes,9"`  // list of non-zero delta counters from TxAMPDU
	//TxAMPDUSuccDelta MCSRateCounters `protobuf:"bytes,10"` // list of non-zero delta counters from TxAMPDUSucc
}

// per-[stream-1][mcs rate] counters. this is a named type because it requires a custom Protobuf marshaller
type MCSRateCounters [][]uint32 // note that when encoding in protobuf, counts which are zero may be omitted. thus when receiving any missing counts must be assumed to be zero

func (r *MCSRateCounters) MarshalProtobuf3() ([]byte, error) {
	// marshal each stream's counters as a repeated value with ID=NumSpatialStreams, and values encoded as varints
	// as if the datastructure were
	//   type MCSRateCounters struct {
	//      ss1 []uint32 `protobuf:"varint,1,packed"`
	//      ss2 []uint32 `protobuf:"varint,2,packed"`
	//      ss3 []uint32 `protobuf:"varint,3,packed"`
	//      ss4 []uint32 `protobuf:"varint,4,packed"`
	//      ...
	//   }
	var buf, tmp protobuf3.Buffer
	for ssi, mcs := range *r {
		// find the index of the last non-zero count
		var last = -1
		for i, cnt := range mcs {
			if cnt != 0 {
				last = i
			}
		}
		if last < 0 {
			// all counts are zero; we can skip this spatial stream entirely
			continue
		}
		// encode from [0] to [last]. the trailing zero counts are omitted
		tmp.Reset()
		for _, cnt := range mcs[:last+1] {
			tmp.EncodeVarint(uint64(cnt))
		}
		buf.EncodeBytes(uint32(ssi)+1, tmp.Bytes())
	}
	return buf.Bytes(), nil
}

// express the MCSRateCounters type returned by MCSRateCounters.MarshalProtobuf3() in protobuf v3 language
func (*MCSRateCounters) AsProtobuf3() (string, string) {
	return "MCSRateCounters", `message MCSRateCounters {
  repeated uint32 ss1 = 1;
  repeated uint32 ss2 = 2;
  repeated uint32 ss3 = 3;
  repeated uint32 ss4 = 4;
  /// our devices don't support more than 4 spatial streams
}`
}

func (r *MCSRateCounters) UnmarshalProtobuf3(data []byte) error {
	buf := protobuf3.NewBuffer(data)
	var err error
outer_loop:
	for !buf.EOF() {
		var x uint64
		x, err = buf.DecodeVarint()
		if err != nil {
			break
		}
		ssi := int(x>>3 - 1)     // index into *r where this SS should go
		if ssi < 0 || ssi >= 8 { // permit a maximum of 8 spatial streams, which is the max the .11ac standard defines (and from what I google, .11ax didn't add more, but I don't have the standard)
			err = fmt.Errorf("Invalid SS count %d", ssi+1)
			break
		}

		// extend *r to contain this spatial stream index
		for len(*r) <= ssi {
			*r = append(*r, nil)
		}

		var ss_data []byte
		ss_data, err = buf.DecodeRawBytes()
		if err != nil {
			break
		}

		var counts []uint32
		tmp := protobuf3.NewBuffer(ss_data)
		for !tmp.EOF() {
			x, err = tmp.DecodeVarint()
			if err != nil {
				break outer_loop
			}
			counts = append(counts, uint32(x))
		}
		(*r)[ssi] = counts
	}
	return err
}

// client capabilites on association
type ClientCapabilities struct {
	MAC        ethernet.MAC `protobuf:"bytes,1"`  // the client's MAC address (at least the one the client is using to associate with this AP; some clients vary their MACs between associations)
	RadioIndex uint8        `protobuf:"varint,2"` // index into the RadioStats array of the radio the client is associated with
	WLAN       struct {
		ID   uuid.UUID `protobuf:"bytes,1"` // id of wlan the client is associated with (unambiguous; for computers)
		SSID string    `protobuf:"bytes,2"` // SSID of wlan the client is associated with (ambiguous; for humans)
	} `protobuf:"bytes,3"`
	Protocol          string  `protobuf:"bytes,4"`   // 802.11 protocol type for example abg, n, ac
	NumSpatialStreams int     `protobuf:"varint,5"`  // total number of spatial streams
	BandWidth20       bool    `protobuf:"varint,6"`  // 20MHz supported
	BandWidth40       bool    `protobuf:"varint,7"`  // 40MHz supported
	BandWidth80       bool    `protobuf:"varint,8"`  // 80MHz supported
	BandWidth160      bool    `protobuf:"varint,9"`  // 160MHz supported
	BandWidth80p80    bool    `protobuf:"varint,10"` // 80+80MHz supported
	SUBeamformee      bool    `protobuf:"varint,11"` // Single User Beamformee
	SUBeamformer      bool    `protobuf:"varint,12"` // Single User Beamformer
	MUBeamformee      bool    `protobuf:"varint,13"` // Multiuser User Beamformee
	MUBeamformer      bool    `protobuf:"varint,14"` // Multiuser Beamformer
	RadioMfgOUI       [3]byte `protobuf:"bytes,15"`  // Broadcom IE present

}

type PwrSaveStats struct {
	Sleeps    uint32 `protobuf:"varint,1"` // client set P bit on a transmitted packet, most likely NULL
	Wakes     uint32 `protobuf:"varint,2"` // client clr P bit on a transmitted packet
	TxPktAged uint32 `protobuf:"varint,3"` // Tx Packet Aged (Freed) client in power save mode
	SetBcnPvb uint32 `protobuf:"varint,4"` // set the Parital Virtual Bit in the Beacon to tell client to wake
	ClrBcnPvb uint32 `protobuf:"varint,5"` // clr the Parital Virtual Bit in the Beacon
}

type UtilizationStats struct {
	All         float32 `protobuf:"fixed32,1"` // channel utilization for any reason (wifi or non-wifi) expressed as a percentage between 0 - 1.0 (a roll-up of the
	Tx          float32 `protobuf:"fixed32,2"` // channel utilization for transmit by this radio (implicitly InBSS). 0 - 1.0
	RxInBSS     float32 `protobuf:"fixed32,3"` // channel utilization for receive by this radio of packets destined for this radio. 0 - 1.0
	RxOtherBSS  float32 `protobuf:"fixed32,4"` // channel utilization for receive by this radio of packets destined for other radios. 0 - 1.0
	UnknownWifi float32 `protobuf:"fixed32,5"` // channel utilization for wifi packets of unknown type (non of the 3 preceeding). 0 - 1.0
	NonWifi     float32 `protobuf:"fixed32,6"` // channel utilization for non-wifi data. 0 - 1.0
}

type UtilizationResults struct {
	Mean      UtilizationStats `protobuf:"bytes,1"`  // Mean of total samples in period
	Min       UtilizationStats `protobuf:"bytes,2"`  // Minimum of all samples in period
	Max       UtilizationStats `protobuf:"bytes,3"`  // Maximum of all samples in period
	PeriodSec int              `protobuf:"varint,4"` // sample period in seconds
	Count     int              `protobuf:"varint,5"` // number of samples
}

// CPUStatFields defines the slice of field strings from a 'cpu*' keyed line from '/proc/stat'
type CPUStatFields [11]string

// NewCPUStatFields builds a new CPUStatFields instance from the given slice of field strings
func NewCPUStatFields(fields []string) (CPUStatFields, error) {
	// validate the given fields
	cpuFields := CPUStatFields{}
	numCPUFields := len(cpuFields)
	if len(fields) != numCPUFields {
		return cpuFields, fmt.Errorf("cannot create new cpu stat fields; invalid number fields given (%d)", len(fields))
	}

	// build and return the cpu stat fields
	for i := 0; i < numCPUFields; i++ {
		cpuFields[i] = fields[i]
	}
	return cpuFields, nil
}

// CPUStats defines the 'cpu*' columns found in '/proc/stat'
type CPUStats struct {
	Name      string `protobuf:"bytes,1"`   // cpu name (total = 'cpu'; individual = 'cpu0', 'cpu1', ...)
	User      uint64 `protobuf:"varint,2"`  // normal processes executing in user mode
	Nice      uint64 `protobuf:"varint,3"`  // niced processes executing in user mode
	System    uint64 `protobuf:"varint,4"`  // processes executing in kernel mode
	Idle      uint64 `protobuf:"varint,5"`  // twiddling thumbs
	IOWait    uint64 `protobuf:"varint,6"`  // waiting for I/O to complete
	IRQ       uint64 `protobuf:"varint,7"`  // servicing interrupts
	SoftIRQ   uint64 `protobuf:"varint,8"`  // servicing softirqs
	Steal     uint64 `protobuf:"varint,9"`  // involuntary wait
	Guest     uint64 `protobuf:"varint,10"` // running a normal guest
	GuestNice uint64 `protobuf:"varint,11"` // running a niced guest
}

// NewCPUStats creates a new 'CPUStats' instance from the given cpu '/proc/stat' text line
func NewCPUStats(line string) (*CPUStats, error) {
	// get the line fields and drop empty lines
	fields := strings.Fields(line)
	numFields := len(fields)
	if numFields <= 0 {
		return &CPUStats{}, fmt.Errorf("cannot parse /proc/stat cpu line; no fields ('%s')", line)
	}

	// validate the fields
	if numFields != 11 {
		return &CPUStats{}, fmt.Errorf("cannot parse /proc/stat cpu line; invalid number of fields (%d)", numFields)
	}

	// convert each string field to a uint64 value
	vals := make([]uint64, numFields)
	for i := 1; i < numFields; i++ {
		v, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			// cannot convert string to uint64
			return &CPUStats{}, fmt.Errorf("cannot convert cpu stat field '%s' (index=%d) to uint64", fields[i], i)
		}
		vals[i-1] = v
	}

	// build and return the cpu stats struct
	return &CPUStats{
		Name:      fields[0],
		User:      vals[0],
		Nice:      vals[1],
		System:    vals[2],
		Idle:      vals[3],
		IOWait:    vals[4],
		IRQ:       vals[5],
		SoftIRQ:   vals[6],
		Steal:     vals[7],
		Guest:     vals[8],
		GuestNice: vals[9],
	}, nil
}

// per wifi radio statistics
type RadioStats struct {
	Dev              string       `protobuf:"bytes,1"`   // net_device name ("r0")
	Band             wifi.Band    `protobuf:"varint,2"`  // the radio band - "2.4" or "5"
	Channel          wifi.Channel `protobuf:"varint,3"`  // Wifi channel number
	SecondaryChannel wifi.Channel `protobuf:"varint,4"`  // 0 usually, secondary Wifi channel number in 2.4Ghz or 80+80 second channel block
	Bandwidth        wifi.Width   `protobuf:"varint,5"`  // 20Mhz, 40Mhz, 80Mhz, 159 means 80 + 80, will be 160 if we get a radio capable of 160Mhz
	PhyType          wifi.PhyType `protobuf:"varint,26"` // PHY type of radio PHY (HT,VHT, or whatever 11ax will be called)

	NoiseFloor  wifi.RSSI `protobuf:"zigzag32,6"` // measured by the radio in dBmW
	TxPower     wifi.RSSI `protobuf:"zigzag32,7"` // Last Transmit power dBmW (rate dependent)
	MaxTxPower  wifi.RSSI `protobuf:"zigzag32,8"` // maximum Transmit power dBmW
	Utilization struct {
		All         float64 `protobuf:"fixed64,1"` // channel utilization for any reason (wifi or non-wifi) expressed as a percentage between 0 - 1.0 (a roll-up of the
		Tx          float64 `protobuf:"fixed64,2"` // channel utilization for transmit by this radio (implicitly InBSS). 0 - 1.0
		RxInBSS     float64 `protobuf:"fixed64,3"` // channel utilization for receive by this radio of packets destined for this radio. 0 - 1.0
		RxOtherBSS  float64 `protobuf:"fixed64,4"` // channel utilization for receive by this radio of packets destined for other radios. 0 - 1.0
		UnknownWifi float64 `protobuf:"fixed64,5"` // channel utilization for wifi packets of unknown type (non of the 3 preceeding). 0 - 1.0
		NonWifi     float64 `protobuf:"fixed64,6"` // channel utilization for non-wifi data. 0 - 1.0
	} `protobuf:"bytes,16"`
	ChanUtil UtilizationResults `protobuf:"bytes,30"` // Channel Utilization results - samples taken over a period of time

	PerAntennaRSSI []wifi.RSSI `protobuf:"zigzag32,10"` // signal strength of last received packet (of any kind, from any client) on each antenna in dBmW
	DFS            struct {    // DFS status
		RadarDetected bool  `protobuf:"varint,1"` // true when radar is detected. I'm not sure how this works if the fallback channel is also a DFS channel
		CacState      uint8 `protobuf:"varint,2"` // raw BRCM WL_DFS_CACSTATES value
	} `protobuf:"bytes,17"`
	Counter struct { // TX/RX counters
		TxBytes       uint64 `protobuf:"varint,1"`  // total bytes, including TxMcastBytes
		TxPkts        uint32 `protobuf:"varint,2"`  // total packets, including TxMcastPkts from the wl interface
		TxMgmt        uint32 `protobuf:"varint,3"`  // management frames
		TxErrors      uint32 `protobuf:"varint,4"`  // total tx errors - majority txnobuf, others txnoassoc, txunderflow, txrunt, txdma err(descriptor err and proto err, and data err)
		RxBytes       uint64 `protobuf:"varint,5"`  // total bytes, including RxMcastBytes
		RxPkts        uint32 `protobuf:"varint,6"`  // total packets, including RxMcastPkts
		RxMgmt        uint32 `protobuf:"varint,7"`  // management frames
		RxErrors      uint32 `protobuf:"varint,8"`  // total rx errors - rxoverflow, rxnobuf, rxfragerr, rxrunt, rxgiant, rxnoscb, rxbadscrmac
		TxFailedArp   uint32 `protobuf:"varint,9"`  // TX Arp failures - txphy error or no ACK
		TxDropArp     uint32 `protobuf:"varint,10"` // TX Arp dropped - in ampdu
		TxPSBlkFifo   uint32 `protobuf:"varint,11"` // TX Power Save Data Fifo block/stuck timeout
		TxToss        uint32 `protobuf:"varint,12"` // TX packet Toss total
		TxTossArp     uint32 `protobuf:"varint,13"` // TX Toss Arp
		TxTossBCMC    uint32 `protobuf:"varint,14"` // TX Toss Bcast/Mcast
		TxTossUnicast uint32 `protobuf:"varint,15"` // TX Toss Unicast (SCB)
		//TxBCMC               uint32 `protobuf:"varint,16"` // TX Bcast/Mcast - redundant get info from WLAN TxMcast
		TxBCMC2Unicast       uint32 `protobuf:"varint,17"` // total TX BCMC packet that was placed on each client unicast queue at interface before tx status
		TxBCMC2UnicastClient uint32 `protobuf:"varint,18"` // total TX Client unicast packets from a BCMC packet at interface before tx status
		TxBcnIntr            uint32 `protobuf:"varint,20"` // total beacon interrupts TBTT - Target Beacon Transmission Time
		TxPhyErr             uint32 `protobuf:"varint,21"` // total TX PhyErr
		TxFailed             uint32 `protobuf:"varint,22"` // total TX packet - no ACK or txphy error
		TxRetries            uint32 `protobuf:"varint,23"` // how many times a packet was sent with the retry bit set (so 3 tx attempts increments this by 2)
		TxRetried            uint32 `protobuf:"varint,24"` // how many packets were retried once or more (so 3 tx attempts only increments this by 1)
		RxDups               uint32 `protobuf:"varint,25"` // total Rx packet Duplicates
		RxRetried            uint32 `protobuf:"varint,26"` // total Rx packet with Retry bit set in 802.11 header
		RxDecryptFailures    uint32 `protobuf:"varint,48"` // number of packets we couldn't decrypt successfully (most likely by hardware MacStat/ucode stat)

		TxBCMC2UnicastBytes       uint64 `protobuf:"varint,27"` // total TX BCMC bytes that was placed on each client unicast queue at interface before tx status
		TxBCMC2UnicastClientBytes uint64 `protobuf:"varint,28"` // total TX Client unicast bytes from a BCMC packet at interface before tx status
		ReInit                    uint32 `protobuf:"varint,29"` // Fatal Error Radio CORE reinitialized
		ReInitThrottle            uint32 `protobuf:"varint,30"` // Fatal Error Radio CORE reinitialized Throttled
		TxMgmtDropped             uint32 `protobuf:"varint,31"` // TX dropped management frames
		RxFifoOverflow            uint32 `protobuf:"varint,32"` // Rx Fifo Overflow (delta)
		RxHlFifoOverflow          uint32 `protobuf:"varint,33"` // Rx Hl Fifo Overflow (delta)

		//RxHWProbeReq             uint32 `protobuf:"varint,34"` // Rx HW Probe Request
		//RxHWProbeReqQOverflow    uint32 `protobuf:"varint,35"` // Rx HW Probe Request Queue Overflow
		//TxHWProbeResp            uint32 `protobuf:"varint,36"` // Tx HW Probe Response Successful
		//TxHWProbeRespFailed      uint32 `protobuf:"varint,37"` // Tx HW Probe Response Failed - NO ACK
		//TxHWProbeRespDropTimeout uint32 `protobuf:"varint,38"` // Tx HW Probe Response Dropped, unable to send in time limit

		BlockDataFifo   uint32 `protobuf:"varint,39"` // Block DataFifo status
		TxNoBuf         uint32 `protobuf:"varint,40"` // tx out of buffers errors
		RxNoBuf         uint32 `protobuf:"varint,41"` // rx out of buffers errors
		TxPktsSent      uint32 `protobuf:"varint,42"` // total packets sent, including Mcast - tx status stats
		TxPktsSentMcast uint32 `protobuf:"varint,43"` // total Mcast packets sent - tx status stats

		RxTossRunt        uint32 `protobuf:"varint,44"` // Toss Rx Pkt too small runt
		RxTossShortStatus uint32 `protobuf:"varint,45"` // Toss Rx Pkt short rx Status - missing PHY Rx Status field
		Down              uint32 `protobuf:"varint,46"` // Radio brought down
		Up                uint32 `protobuf:"varint,47"` // Radio brought up

		TxBcnInactivity uint32 `protobuf:"varint,49"` // Tx Beacon Inactivity Detected

	} `protobuf:"bytes,18"`

	MacStats struct { // mac (ucode) stats
		TxAllFrm      uint32    `protobuf:"varint,1"`  //total number of frames sent, incl. Data, ACK, RTS, CTS, Control Management (includes retransmissions)
		TxRtsFrm      uint32    `protobuf:"varint,2"`  // number of RTS sent out by the MAC
		TxCtsFrm      uint32    `protobuf:"varint,3"`  // number of CTS sent out by the MAC
		TxDataNullFrm uint32    `protobuf:"varint,4"`  // number of Null-Data transmission generated from template
		TxBcnFrm      uint32    `protobuf:"varint,5"`  // beacons transmitted
		TxAmpdu       uint32    `protobuf:"varint,6"`  // number of AMPDUs transmitted
		TxMpdu        uint32    `protobuf:"varint,7"`  // number of MPDUs transmitted
		TxUnfl        [6]uint32 `protobuf:"varint,8"`  // per-fifo tx underflows
		TxBcnUnfl     uint32    `protobuf:"varint,9"`  // Template underflows (mac was too slow to transmit ACK/CTS or BCN)
		TxPhyErr      uint32    `protobuf:"varint,10"` // Transmit phy error, type of error is reported in tx-status for driver enqueued frames

		RxUcastPktEng uint32 `protobuf:"varint,11"` // unicast frames rxed by the pkteng code
		RxMcastPktEng uint32 `protobuf:"varint,12"` // multicast frames rxed by the pkteng code
		RxFrmTooLong  uint32 `protobuf:"varint,13"` // Received frame longer than legal limit (2346 bytes)
		RxFrmTooShort uint32 `protobuf:"varint,14"` // Received frame did not contain enough bytes for its frame type

		RxAnyErr  uint32 `protobuf:"varint,15"` // Any RX error that is not counted by other counters.
		RxBadFcs  uint32 `protobuf:"varint,16"` // number of frames for which the CRC check failed in the MAC
		RxBadPlcp uint32 `protobuf:"varint,17"` // parity check of the PLCP header failed
		RxGlitch  uint32 `protobuf:"varint,18"` // PHY was able to correlate the preamble but not the header

		RxStart uint32 `protobuf:"varint,19"` // Number of received frames with a good PLCP (i.e. passing parity check)

		RxUcastMbss   uint32 `protobuf:"varint,20"` // number of received DATA frames with good FCS and matching RA
		RxMgmtMbss    uint32 `protobuf:"varint,21"` // number of received mgmt frames with good FCS and matching RA
		RxCtl         uint32 `protobuf:"varint,22"` // number of received CNTRL frames with good FCS and matching RA
		RxRts         uint32 `protobuf:"varint,23"` // number of unicast RTS addressed to the MAC (good FCS)
		RxCts         uint32 `protobuf:"varint,24"` // number of unicast CTS addressed to the MAC (good FCS)
		RxAck         uint32 `protobuf:"varint,25"` // number of ucast ACKS received (good FCS)
		RxUcastNA     uint32 `protobuf:"varint,26"` // number of received DATA frames (good FCS and not matching RA)
		RxMgmtNA      uint32 `protobuf:"varint,27"` // number of received MGMT frames (good FCS and not matching RA)
		RxCtlNA       uint32 `protobuf:"varint,28"` // number of received CNTRL frame (good FCS and not matching RA)
		RxRtsNA       uint32 `protobuf:"varint,29"` // number of received RTS not addressed to the MAC
		RxCtsNA       uint32 `protobuf:"varint,30"` // number of received CTS not addressed to the MAC
		RxDataMcast   uint32 `protobuf:"varint,31"` // number of RX Data multicast frames received by the MAC
		RxMgmtMcast   uint32 `protobuf:"varint,32"` // number of RX Management multicast frames received by the MAC
		RxCtlMcast    uint32 `protobuf:"varint,33"` // number of RX Control multicast frames received by the MAC
		RxBcnMbss     uint32 `protobuf:"varint,34"` // beacons received from member of BSS
		RxUcastObss   uint32 `protobuf:"varint,35"` // number of unicast frames addressed to the MAC from other BSS (WDS FRAME)
		RxBcnObss     uint32 `protobuf:"varint,36"` // beacons received from other BSS
		RxRspTimeout  uint32 `protobuf:"varint,37"` // number of response timeouts for transmitted frames expecting a response
		TxBcnCancel   uint32 `protobuf:"varint,38"` // transmit beacons canceled due to receipt of beacon (IBSS)
		RxNoDelimiter uint32 `protobuf:"varint,39"` // number of no valid delimiter detected by ampdu parser
		Rxf0ovfl      uint32 `protobuf:"varint,40"` // number of receive fifo 0 overflows
		Rxf1ovfl      uint32 `protobuf:"varint,41"` // number of receive fifo 1 overflows
		Rxhlovfl      uint32 `protobuf:"varint,42"` // number of length / header fifo overflows
		MissBcnDbg    uint32 `protobuf:"varint,43"` // number of beacon missed to receive
		Pmqovfl       uint32 `protobuf:"varint,44"` // number of PMQ overflows

		RxHWProbeReq             uint32 `protobuf:"varint,45"` // Rx HW Probe Request
		RxHWProbeReqQOverflow    uint32 `protobuf:"varint,46"` // Rx HW Probe Request Queue Overflow
		TxHWProbeRespFailed      uint32 `protobuf:"varint,47"` // Tx HW Probe Response Failed - NO ACK
		TxHWProbeResp            uint32 `protobuf:"varint,48"` // Tx HW Probe Response Successful
		TxHWProbeRespDropTimeout uint32 `protobuf:"varint,49"` // Tx HW Probe Response Dropped, unable to send in time limit

		TxRtsFail   uint32 `protobuf:"varint,50"` //number of rts transmission failure that reach retry limit
		TxUcast     uint32 `protobuf:"varint,51"` // number of unicast tx expecting response other than cts/cwcts
		TxInRtsTxop uint32 `protobuf:"varint,52"` // number of data frame transmissions during rts txop
		RxBlockAck  uint32 `protobuf:"varint,53"` // blockack rxcnt
		TxBlockAck  uint32 `protobuf:"varint,54"` // blockack txcnt

		PhyRxGlitch uint32 `protobuf:"varint,55"` // PHY count of bphy glitches
		RxDrop2nd   uint32 `protobuf:"varint,56"` // drop secondary cnt
		RxTooLate   uint32 `protobuf:"varint,57"` // receive too late
		PhyBadPlcp  uint32 `protobuf:"varint,58"` // number of bad PLCP reception on BPHY rate
	} `protobuf:"bytes,31"`

	//Ampdu AmpduCounter `protobuf:"bytes,19"` // Ampdu TX/Rx counters
	AmpduTx      AmpduTxCounter `protobuf:"bytes,20"`  // Ampdu TX counters
	AmpduRx      AmpduRxCounter `protobuf:"bytes,21"`  // Ampdu RX counters
	NumClients   uint32         `protobuf:"varint,22"` // Number of associated WC for radio from all WLANs
	RadioMissing bool           `protobuf:"varint,23"` // Use to indicate a radio is missing. most likely on purpose by us.

	InterruptStats struct {
		TxBcnSucc uint32 `protobuf:"varint,1"` // Tx Beacon Success
		DMAInt    uint32 `protobuf:"varint,2"` // DMA into RX FIFO - Rx Packet or TX Status
		TxStatus  uint32 `protobuf:"varint,3"` // DMA into RX FIFO - TX Status
	} `protobuf:"bytes,24"`

	// NAR - Non Aggregated Regulation
	NonAggrRegulate NarStats `protobuf:"bytes,25"`

	ProbeCounter struct { // these are from the radio driver
		RxProbeReqBC          uint32 `protobuf:"varint,1"` // Rx Probe Request Broadcast DA
		RxProbeReqNonBC       uint32 `protobuf:"varint,2"` // Rx Probe Request Non-Broadcast DA
		IgnoreProbeReqMinRSSI uint32 `protobuf:"varint,3"` // ignore (don't send probe resp) if below min RSSI
		TxProbeRespSW         uint32 `protobuf:"varint,4"` // send SW probe response
		RxProbeReqRand        uint32 `protobuf:"varint,5"` // Rx Probe Request Broadcast DA
	} `protobuf:"bytes,27"`

	Pq      PktQueueStats `protobuf:"bytes,28"` // Radio Packet Queue Stats
	PktPend PktPendStats  `protobuf:"bytes,29"` // Radio TX Packet Pending Stats - per type and module

	MacSuspStats struct { // Suspend MAC and Wait stats
		Spin uint32 `protobuf:"varint,1"` // suspend macm, spin and wait
		Fail uint32 `protobuf:"varint,2"` // suspend mac and wait timeout failure
	} `protobuf:"bytes,32"`

	ScbStats struct { // Scb - station control block in radio
		Reclaim     uint32 `protobuf:"varint,1"` // times had to reclaim a scb reached max scb
		ReclaimNone uint32 `protobuf:"varint,2"` // times reclaim failed
	} `protobuf:"bytes,33"`

	QoS [AC_COUNT]QoSStats `protobuf:"bytes,34"` // Tx and Rx counters per QOS type
}

type RdRoamNotifyStats struct {
	Dev      string `protobuf:"bytes,1"`  // net_device name ("r0")
	RxPkts   uint32 `protobuf:"varint,2"` // total received roam notification packets since last report
	TxPkts   uint32 `protobuf:"varint,3"` // total sent roam notification packets since last report
	WCRoamed uint32 `protobuf:"varint,4"` // total WC associated in radio that were disassociated due to roam notification received since last report
}

type WifiChannelStats struct {
	Channel    wifi.ChannelSet `protobuf:"bytes,1"`
	NoiseFloor wifi.RSSI       `protobuf:"zigzag32,2"` // measured by the radio in dBmW
	Congest    struct {
		RxInBSS    float32 `protobuf:"fixed32,1"` // channel congestion for receive by this radio of packets destined for this radio. 0 - 1.0
		RxOtherBSS float32 `protobuf:"fixed32,2"` // channel congestion for receive by this radio of packets destined for other radios. 0 - 1.0
		NonWifi    float32 `protobuf:"fixed32,3"` // channel congestion for non-wifi 802.11 data. 0 - 1.0
	} `protobuf:"bytes,3"`
	Utilization struct {
		All         float32 `protobuf:"fixed32,1"` // channel utilization for any reason (wifi or non-wifi) expressed as a percentage between 0 - 1.0 (a roll-up of the
		Tx          float32 `protobuf:"fixed32,2"` // channel utilization for transmit by this radio (implicitly InBSS). 0 - 1.0
		RxInBSS     float32 `protobuf:"fixed32,3"` // channel utilization for receive by this radio of packets destined for this radio. 0 - 1.0
		RxOtherBSS  float32 `protobuf:"fixed32,4"` // channel utilization for receive by this radio of packets destined for other radios. 0 - 1.0
		UnknownWifi float32 `protobuf:"fixed32,5"` // channel utilization for wifi packets of unknown type (non of the 3 preceeding). 0 - 1.0
		NonWifi     float32 `protobuf:"fixed32,6"` // channel utilization for non-wifi data. 0 - 1.0
	} `protobuf:"bytes,4"`
	When time.Time `protobuf:"bytes,5"` // when these stats were gathered
}

type BTRadioStats struct {
	BLE BLEStats `protobuf:"bytes,1"`
}

type BLEStats struct {
	MAC  ethernet.MAC `protobuf:"bytes,1"`
	UUID uuid.UUID    `protobuf:"bytes,2"` // BLE uuid

	// from hciconfig - BT radio stats
	TxErrors   uint32 `protobuf:"varint,16"`
	TxBytes    uint32 `protobuf:"varint,3"`
	TxCommands uint32 `protobuf:"varint,4"`
	RxErrors   uint32 `protobuf:"varint,17"`
	RxBytes    uint32 `protobuf:"varint,5"`
	RxEvents   uint32 `protobuf:"varint,6"`

	TxAdvert         uint32 `protobuf:"varint,7"`  // count of TX Advertisements
	TxBeaconCyclical uint32 `protobuf:"varint,8"`  // count of cyclical beacons
	TxAdvertIBeacon  uint32 `protobuf:"varint,20"` // count of iBeacon advertisements
	TxAdvertEddyUid  uint32 `protobuf:"varint,21"` // count of Eddystone-UID advertisements
	TxAdvertEddyUrl  uint32 `protobuf:"varint,22"` // count of Eddystone-URL advertisements

	// LE Scan stats
	Scan             uint32 `protobuf:"varint,9"`  // count of RX BLE Scans
	RxScanPkts       uint32 `protobuf:"varint,10"` // count of RX BLE beacons and scan resps during a LE scan
	RxScanPktsRandom uint32 `protobuf:"varint,11"` // count of RX BLE beacons and scan resps from a random MAC during a LE scan
	RxScanResp       uint32 `protobuf:"varint,12"` // count of RX BLE scan responds during a LE scan
	RxAdvert         uint32 `protobuf:"varint,13"` // count of RX BLE Advertisements during a LE scan

	ResetHungStats  uint32 `protobuf:"varint,18"` // count of Hardware being reset due to hung stats
	ResetHungRxScan uint32 `protobuf:"varint,19"` // count of Hardware being reset due to hung RX during a scan

}

type ProbeStats struct {
	Dev                   string                  `protobuf:"bytes,1"`  // net_device name ("r0")
	Band                  wifi.Band               `protobuf:"varint,2"` // the device's radio band
	Channels              []wifi.Channel          `protobuf:"varint,3"` // Wifi channel number(s), a channel list for monitor
	Bandwidth             wifi.Width              `protobuf:"varint,4"` // 20Mhz, 40Mhz, 80Mhz, 159 means 80 + 80, will be 160 if we get a radio capable of 160Mhz
	RadioMAC              ethernet.MAC            `protobuf:"bytes,5"`  // radio MAC
	RxProbeReqPkts        uint32                  `protobuf:"varint,6"` // Rx Probe Request
	RxProbeReqRandom      uint32                  `protobuf:"varint,7"` // Rx Probe Request Random mac
	RxProbeReqBcastSSID   uint32                  `protobuf:"varint,8"` // Rx Probe Request broadcast SSID
	RxProbeReqOffChan     uint32                  `protobuf:"varint,9"` // Rx Probe Request off channel
	ProbeRequestList      []ProbeRequest          `protobuf:"bytes,10"` // Probe Request List
	ProbeRequestRssiStats []ProbeRequestRssiStats `protobuf:"bytes,11"` // Probe Request Rssi Stats
}

// per wifi WLAN, per Radio statistics
type WLANStats struct {
	ID         uuid.UUID    `protobuf:"bytes,1"`  // id of this wlan
	RadioIndex uint8        `protobuf:"varint,2"` // index into the RadioStats array of the radio this WLAN is using
	BSSID      ethernet.MAC `protobuf:"bytes,3"`  // BSSID of this WLAN on that radio. Note that WLANs can be assigned to just one or many radios
	Dev        string       `protobuf:"bytes,4"`  // name of net_device ("wl0.1")
	SSID       string       `protobuf:"bytes,5"`  // ambiguous; just for the humans
	ForMesh    bool         `protobuf:"varint,9"` // whether this WLAN is being served as Mesh Uplink

	NumClients uint32 `protobuf:"varint,6"`
	// NOTE: the clients' stats are found in the APStats.Clients[] list

	TxMcastPkts  uint32 `protobuf:"varint,7"` // multicast & broadcast both
	TxMcastBytes uint64 `protobuf:"varint,8"` // multicast & broadcast both

	RadiusAuth []RadiusAuthStats `protobuf:"bytes,10"` // radius auth server stats
	RadiusAcct []RadiusAcctStats `protobuf:"bytes,11"` // radius acct server stats
}

// Radius auth server stats. One instance per configured Radius auth server
type RadiusAuthStats struct {
	IP                 string `protobuf:"bytes,1"`   // radius server ip-address
	Port               uint16 `protobuf:"varint,2"`  // radius server port
	Requests           uint32 `protobuf:"varint,3"`  // radius client requests
	Retransmissions    uint32 `protobuf:"varint,4"`  // radius client retransmissions
	AccessAccepts      uint32 `protobuf:"varint,5"`  // radius client access accepts
	AccessRejects      uint32 `protobuf:"varint,6"`  // radius client access rejects
	AccessChallenges   uint32 `protobuf:"varint,7"`  // radius client access challenges
	Responses          uint32 `protobuf:"varint,8"`  // radius client responses
	MalformedResponses uint32 `protobuf:"varint,9"`  // radius client malformed responses
	BadAuthenticators  uint32 `protobuf:"varint,10"` // radius client bad authenticators
	Timeouts           uint32 `protobuf:"varint,11"` // radius client timeouts
	UnknownTypes       uint32 `protobuf:"varint,12"` // radius client unknown types
	PacketsDropped     uint32 `protobuf:"varint,13"` // radius client packets dropped
	RoundTripTimeMs    uint32 `protobuf:"varint,14"` // radius client round-trip time (in msec)
}

// Radius acct server stats. One instance per configured Radius acct server
type RadiusAcctStats struct {
	IP                 string `protobuf:"bytes,1"`   // radius server ip-address
	Port               uint16 `protobuf:"varint,2"`  // radius server port
	Requests           uint32 `protobuf:"varint,3"`  // radius client requests
	Retransmissions    uint32 `protobuf:"varint,4"`  // radius client retransmissions
	Responses          uint32 `protobuf:"varint,5"`  // radius client responses
	MalformedResponses uint32 `protobuf:"varint,6"`  // radius client malformed responses
	Timeouts           uint32 `protobuf:"varint,7"`  // radius client timeouts
	UnknownTypes       uint32 `protobuf:"varint,8"`  // radius client unknown types
	PacketsDropped     uint32 `protobuf:"varint,9"`  // radius client packets dropped
	RoundTripTimeMs    uint32 `protobuf:"varint,10"` // radius client round-trip time (in msec)
}

type ScanResults struct {
	SSID           string          `protobuf:"bytes,1"`
	BSSID          ethernet.MAC    `protobuf:"bytes,2"`
	RSSI           wifi.RSSI       `protobuf:"zigzag32,3"`
	Channel        wifi.ChannelSet `protobuf:"bytes,4"`  // note: name of field is historical. don't touch.
	PotentialRogue bool            `protobuf:"varint,5"` // potential rogue as detected by the AP
	IsSpoof        bool            `protobuf:"varint,6"` // is spoofing the reporting AP
	ListenChannel  wifi.Channel    `protobuf:"varint,7"` // listening channel (RX channel)
}

type WLANScanResults struct {
	BSSID   ethernet.MAC `protobuf:"bytes,1"`
	RSSISum int32        `protobuf:"zigzag32,2"` // Sum of the RSSIs of all the packets heard
	RSSICnt uint32       `protobuf:"varint,3"`   // number of packets
}

// found in ap-wifi-scan-results- topic
type WifiRadioScanResults struct {
	ID                  ethernet.MAC       `protobuf:"bytes,1"`   // the base MAC (id) of the EP sending these stats
	ReportAll           bool               `protobuf:"varint,11"` // Scan Results of all APs
	ChannelStatsVersion int                `protobuf:"varint,12"` // ScanChannelStats Version to distinguish which fields are populated
	Uptime              int32              `protobuf:"varint,10"` // uptime since linux boot, in seconds; varint is ok, it shouldn't be negative
	Scan24GHz           []ScanResults      `protobuf:"bytes,7"`
	Scan5GHz            []ScanResults      `protobuf:"bytes,8"`
	ScanChannelStats    []WifiChannelStats `protobuf:"bytes,9"` // wifi scan radio per channel stats: noise floor, cca

	Copied *struct { // the ep-telemetry shuffle's copied values. filled in by ep-terminator
		ID     ethernet.MAC `protobuf:"bytes,1"` // the base MAC (id) of the EP sending these stats
		OrgID  uuid.UUID    `protobuf:"bytes,2"` // the organization-id (who owns the device)
		SiteID uuid.UUID    `protobuf:"bytes,4"` // Site ID configured from the cloud for this AP

		MapID uuid.UUID  `protobuf:"bytes,16"`   // AP location
		XYZ   [3]float64 `protobuf:"fixed64,17"` // relative to the Map's origin

		When time.Time `protobuf:"bytes,5"` // when these stats were gathered

		FirmwareVersion string       `protobuf:"bytes,20"`
		Radios          []RadioStats `protobuf:"bytes,31"`
		Airiq           *AiriqStats  `protobuf:"bytes,40"`
		AiriqRestart    uint32       `protobuf:"varint,41"`
	} `protobuf:"bytes,2016" json:",omitempty"`
	InfoFromTerminator *msgs.InfoFromTerminator `protobuf:"bytes,2047" json:",omitempty"`
}

type PktRSSIData struct {
	RxTime time.Time `protobuf:"bytes,1"`    // Timestamp at which this packet was recorded
	RSSI   wifi.RSSI `protobuf:"zigzag32,2"` // RSSI value of the packet
}

type MinorRSSIData struct {
	Minor    uint16        `protobuf:"varint,1"` // Minors from vBlE, Multi-beacon, Cyclical ibeacon
	RSSIList []PktRSSIData `protobuf:"bytes,2"`  // List of RSSI values and times they were received for this Minor
}

type InstanceRSSIData struct {
	Instance string        `protobuf:"bytes,1"` // Minors from vBlE,Multi-beacon,Cyclical ibeacon with above UUID
	RSSIList []PktRSSIData `protobuf:"bytes,2"` // List of RSSI values and times they were received for this Instance
}

/* BLE Scan Results: Advertisement Types and ScanRsp */
type IBeaconAdvert struct {
	RxCnt          uint32          `protobuf:"varint,1"`   // Rx count
	LastRxTime     time.Time       `protobuf:"bytes,2"`    // timestamp of last Rx Pkt
	UUID           uuid.UUID       `protobuf:"bytes,3"`    // UUID from vBlE,Multi-beacon,Cyclical ibeacon or altbeacon
	Major          uint16          `protobuf:"varint,4"`   // last Major from vBlE,Multi-beacon,Cyclical ibeacon or altbeacon
	Minors         []uint16        `protobuf:"varint,5"`   // Minors from vBlE,Multi-beacon,Cyclical ibeacon with above UUID
	SupportsMotion bool            `protobuf:"varint,6"`   // Supports Motion
	TxPower        int8            `protobuf:"zigzag32,7"` // Tx Power from the advertisement pkt
	MinorRSSIList  []MinorRSSIData `protobuf:"bytes,8"`    // List of RSSI values and times they were received
}

type AltBeaconAdvert struct {
	RxCnt      uint32    `protobuf:"varint,1"`   // Rx count
	LastRxTime time.Time `protobuf:"bytes,2"`    // timestamp of last Rx Pkt
	UUID       uuid.UUID `protobuf:"bytes,3"`    // UUID from vBlE altbeacon
	Major      uint16    `protobuf:"varint,4"`   // last Major from vBlE altbeacon
	Minors     []uint16  `protobuf:"varint,5"`   // Minors from vBlE with above UUID
	TxPower    int8      `protobuf:"zigzag32,7"` // Tx Power from the advertisement pkt
}

type EddystoneUIDAdvert struct {
	RxCnt            uint32             `protobuf:"varint,1"`   // Rx count
	LastRxTime       time.Time          `protobuf:"bytes,2"`    // timestamp of last Rx Pkt
	Namespace        string             `protobuf:"bytes,3"`    // Namespace from vBlE or Multi-beacon
	Instances        []string           `protobuf:"bytes,4"`    // Instances from vBlE or Multi-beacon with above Namespace
	TxPower          int8               `protobuf:"zigzag32,7"` // Tx Power from the advertisement pkt
	InstanceRSSIList []InstanceRSSIData `protobuf:"bytes,8"`    // List of RSSI values and times they were received
}

type EddystoneTLMAdvert struct {
	RxCnt        uint32    `protobuf:"varint,1"`  // Rx count
	LastRxTime   time.Time `protobuf:"bytes,2"`   // timestamp of last Rx pkt
	BatteryLevel uint16    `protobuf:"varint,3"`  // BatteryLevel of beacon, in mV
	Temperature  float32   `protobuf:"fixed32,4"` // Temperature detected by the beacon, in Celsius
	AdvCount     uint32    `protobuf:"varint,5"`  // Number of advertisement packets since bootup
	SecCount     float32   `protobuf:"fixed32,6"` // Time since last bootup
}

type EddystoneEncryptedTLMAdvert struct {
	RxCnt      uint32    `protobuf:"varint,1"` // Rx count
	LastRxTime time.Time `protobuf:"bytes,2"`  // timestamp of last Rx pkt
	Salt       uint16    `protobuf:"varint,3"` // 16 bit Salt
	Mic        uint16    `protobuf:"varint,4"` // 16 bit Message Integrity Check
	Etlm       string    `protobuf:"bytes,5"`  // Encrypted TLM Data
}

type EddystoneURLAdvert struct {
	RxCnt      uint32        `protobuf:"varint,1"`   // Rx count
	LastRxTime time.Time     `protobuf:"bytes,2"`    // timestamp of last Rx Pkt
	PrefixURL  string        `protobuf:"bytes,3"`    // Namespace from Multi-beacon
	TxPower    int8          `protobuf:"zigzag32,7"` // Tx Power from the advertisement pkt
	RSSIList   []PktRSSIData `protobuf:"bytes,8"`    // List of RSSI values and times they were received
}

type EddystoneEIDAdvert struct {
	RxCnt      uint32    `protobuf:"varint,1"`   // Rx count
	LastRxTime time.Time `protobuf:"bytes,2"`    // timestamp of last Rx Pkt
	Data       uint64    `protobuf:"varint,3"`   // EID Data
	TxPower    int8      `protobuf:"zigzag32,4"` // Tx Power from the advertisement pkt
}

type BLEScanRsp struct {
	RxCnt      uint32      `protobuf:"varint,1"` // Rx count
	LastRxTime time.Time   `protobuf:"bytes,2"`  // timestamp of last Rx Pkt
	DevUUIDs   []uuid.UUID `protobuf:"bytes,3"`  // device UUID
	DevNames   []string    `protobuf:"bytes,4"`  // device Names
}

type BLEMfgSpecificData struct {
	RxCnt        uint32    `protobuf:"varint,1"` // Rx count
	LastRxTime   time.Time `protobuf:"bytes,2"`  // timestamp of last Rx Pkt
	CompanyIdVal uint16    `protobuf:"varint,3"` // Company Identifier
	MfgData      string    `protobuf:"bytes,5"`  // Mfg Data bytestream
}

type BLEMfgDataFast struct {
	CompanyIdVal uint16 `protobuf:"varint,1"` // Company Identifier
	MfgData      string `protobuf:"bytes,2"`  // Mfg Data bytestream
}

type BLEMistAdv struct {
	RxCnt      uint32    `protobuf:"varint,1"` // Rx count
	LastRxTime time.Time `protobuf:"bytes,2"`  // timestamp of last Rx Pkt
	MistData   string    `protobuf:"bytes,3"`  // Mist pkt bytestream
}

type BLEAaplAdvert struct {
	RxCnt      uint32    `protobuf:"varint,1"` // Rx count
	LastRxTime time.Time `protobuf:"bytes,2"`  // timestamp of last Rx Pkt
	AdvType    string    `protobuf:"bytes,3"`  // Adv Type based on the iBeacon length
	AdvData    string    `protobuf:"bytes,4"`  // advertisment payload
}

type ServiceDataAdvert struct {
	UUID       uuid.UUID `protobuf:"bytes,1"`  // Service UUID
	Data       string    `protobuf:"bytes,2"`  // Service Data
	RxCnt      uint32    `protobuf:"varint,3"` // Rx count
	LastRxTime time.Time `protobuf:"bytes,4"`  // timestamp of last Rx Pkt
}

type SensorDataAdvert struct {
	BatteryLevel uint8  `protobuf:"varint,1"`   // Battery of the device in percentage
	Temperature  int16  `protobuf:"zigzag32,2"` // Surrounding temperature in Celsius (resolution in 0.01 Celsius)
	Humidity     uint16 `protobuf:"varint,3"`   // Surrounding humidity in percentage (resolution in 0.01 percentage)
}

// This information is collected by connecting to the BLE device
// and reading Device Information service if available
type DeviceInfoGap struct {
	ManufacturerName string `protobuf:"bytes,1"` // Name of the device manufacturer
	ModelNumber      string `protobuf:"bytes,2"` // Model number of the device
	SerialNumber     string `protobuf:"bytes,3"` // Serial number of the device
}

// found in ap-ble-scan-results- topic
type BLEScanResults struct {
	/* Info from BLE APs supporting vBLE (Eddystone-UID), Multi-beacon ibeacon, and App Waking (cyclical ibeacon) */
	MAC             ethernet.MAC `protobuf:"bytes,1"`    // BLE device MAC
	RandAddr        bool         `protobuf:"varint,24"`  // false for public, true for random
	RandAddrType    uint8        `protobuf:"varint,29"`  // Type of random address - Static (1), RPA (2), Non-RPA (3)
	UUID            uuid.UUID    `protobuf:"bytes,2"`    // last UUID from BLE device (obsolete)
	DevName         string       `protobuf:"bytes,19"`   // Client BLE device name
	DevNameGatt     string       `protobuf:"bytes,32"`   // BLE device name coming from GATT connection
	Major           uint16       `protobuf:"varint,3"`   // last Major from a BLE device
	Minor           uint16       `protobuf:"varint,4"`   // last Minor from a BLE device
	Beam            uint8        `protobuf:"varint,5"`   // last beam used for the BLE Rx Pkt
	RSSI            wifi.RSSI    `protobuf:"zigzag32,6"` // last rssi used for the BLE Rx Pkt
	RxPktCnt        uint32       `protobuf:"varint,7"`   // Rx Pkt count
	RxMinorPktCnt   uint32       `protobuf:"varint,8"`   // Rx Pkt with Minors count
	LastRxTime      time.Time    `protobuf:"bytes,9"`    // timestamp of last RX pkt either advertisement (w/wout Minor) or scan response no minor
	LastRxMinorTime time.Time    `protobuf:"bytes,10"`   // timestamp of last RX pkt with valid Minor

	ScanRsp         BLEScanRsp                    `protobuf:"bytes,12"` // ScanRsp heard with list of DevUUID
	IBeacons        []IBeaconAdvert               `protobuf:"bytes,13"` // list of iBeacons heard with unique UUID
	EddystoneUIDs   []EddystoneUIDAdvert          `protobuf:"bytes,14"` // list of EddystoneUID heard with unique Namespace
	EddystoneURLs   []EddystoneURLAdvert          `protobuf:"bytes,16"` // list of EddystoneURL heard with unique URL
	AltBeacon       []AltBeaconAdvert             `protobuf:"bytes,17"` // list of AltBeacon heard (vBLE no longer defaults AltBeacon)
	EddystoneTLMs   []EddystoneTLMAdvert          `protobuf:"bytes,18"` // list of EddystoneTLMs heard from this MAC
	EddystoneETLMs  []EddystoneEncryptedTLMAdvert `protobuf:"bytes,20"` // list of Encrypted Eddystone TLMs heard from this MAC
	EddystoneEids   []EddystoneEIDAdvert          `protobuf:"bytes,21"` // list of Eddystone EIDs heard from this MAC
	MfgSpecificData []BLEMfgSpecificData          `protobuf:"bytes,22"` // list of MFG Specific Data
	AaplAdvData     []BLEAaplAdvert               `protobuf:"bytes,23"` // list of Apple Advertising Data (non iBeacon)
	MistAdvData     []BLEMistAdv                  `protobuf:"bytes,33"` // list of Mist Specific BLE pkts

	PktEventType uint8               `protobuf:"varint,25"`   // ADV type: connectable undirected/connectable directed/scannable/non connectable/scan response
	TxPower      int8                `protobuf:"zigzag32,26"` // Transmit power of the advertising device
	Flags        uint8               `protobuf:"varint,27"`   // Advertisement flags
	ServiceData  []ServiceDataAdvert `protobuf:"bytes,28"`    // List of Service UUIDs and their data
	SensorData   SensorDataAdvert    `protobuf:"bytes,30"`    // Sensor information for this BLE device
	DeviceInfo   DeviceInfoGap       `protobuf:"bytes,31"`    // Device Information Service characteristic values

	Copied *struct { // the ep-telemetry shuffle's copied values. filled in by ep-terminator
		ID     ethernet.MAC `protobuf:"bytes,1"` // the base MAC (id) of the EP sending these stats
		OrgID  uuid.UUID    `protobuf:"bytes,2"` // the organization-id (who owns the device)
		SiteID uuid.UUID    `protobuf:"bytes,4"` // Site ID configured from the cloud for this AP

		MapID uuid.UUID  `protobuf:"bytes,16"`   // AP location
		XYZ   [3]float64 `protobuf:"fixed64,17"` // relative to the Map's origin

		When time.Time `protobuf:"bytes,5"` // when these stats were gathered
	} `protobuf:"bytes,2016" json:",omitempty"`
	InfoFromTerminator *msgs.InfoFromTerminator `protobuf:"bytes,2047" json:",omitempty"`
}

type MonitorRssiPkt struct {
	RA          ethernet.MAC `protobuf:"bytes,1"`  // Receiver Address
	TA          ethernet.MAC `protobuf:"bytes,2"`  // Transmiter Address
	TSFT        uint64       `protobuf:"varint,3"` // timestamp from RadioTap
	ClockTime   int64        `protobuf:"varint,4"` // timestamp from pcap
	ChannelFreq uint16       `protobuf:"varint,5"` // channel's center frequency in MHz
	Bandwidth   wifi.Width   `protobuf:"varint,6"`
	RSSI        wifi.RSSI    `protobuf:"zigzag32,7"`
	Dot11FC     uint8        `protobuf:"varint,8"`  // 802.11 Frame Control from packet
	RateFlag    uint8        `protobuf:"varint,9"`  // Rate Flag - bit 0 CCK/ODFM, bit 1 MCS, bit 2 VHT
	Rate        uint8        `protobuf:"varint,10"` // CCK/OFDM, MCS Index, or VHT_MCSNSS

	Copied *struct { // the ep-telemetry shuffle's copied values. filled in by ep-terminator
		ID     ethernet.MAC `protobuf:"bytes,1"` // the base MAC (id) of the EP sending these stats
		OrgID  uuid.UUID    `protobuf:"bytes,2"` // the organization-id (who owns the device)
		SiteID uuid.UUID    `protobuf:"bytes,4"` // Site ID configured from the cloud for this AP

		MapID uuid.UUID  `protobuf:"bytes,16"`   // AP location
		XYZ   [3]float64 `protobuf:"fixed64,17"` // relative to the Map's origin

		When time.Time `protobuf:"bytes,5"` // when these stats were gathered
	} `protobuf:"bytes,2016" json:",omitempty"`
	InfoFromTerminator *msgs.InfoFromTerminator `protobuf:"bytes,2047" json:",omitempty"`
}

type PktRSSI struct {
	RSSI           wifi.RSSI   `protobuf:"zigzag32,1"` // computed RSSI
	PerAntennaRSSI []wifi.RSSI `protobuf:"zigzag32,2"` // raw, per-antenna RSSIS
}

type ProbeRequest struct {
	MAC            ethernet.MAC   `protobuf:"bytes,1"`   // client Address
	RSSIList       []PktRSSI      `protobuf:"bytes,2"`   // RSSI of each packet in list
	ChannelList    []wifi.Channel `protobuf:"varint,3"`  // channel of each packet in list
	SeqNumList     []uint16       `protobuf:"varint,4"`  // seqnum of each packet in list
	SSIDList       []string       `protobuf:"bytes,5"`   // list of SSID heard
	RxListPkts     uint32         `protobuf:"varint,6"`  // number of RX packets heard in AP stats interval
	RxPkts         uint32         `protobuf:"varint,7"`  // running total of all probe requests from client
	BcastSSIDPkts  uint32         `protobuf:"varint,8"`  // running total of Broadcast SSID probe requests
	FirstClockTime int64          `protobuf:"varint,9"`  // nanosecond unix timestamp of first RX packet in list
	LastClockTime  int64          `protobuf:"varint,10"` // nanosecond unix timestamp of last RX packet in list
	LastRxTime     time.Time      `protobuf:"bytes,11"`  // timestamp of last RX
}
type ProbeRequestRssiStats struct {
	MAC            ethernet.MAC `protobuf:"bytes,1"`    // client Address
	RSSISum        int32        `protobuf:"zigzag32,2"` // Sum of the RSSIs of all the packets
	RSSICnt        uint32       `protobuf:"varint,3"`   // number of packets
	LastRSSI       wifi.RSSI    `protobuf:"zigzag32,4"` // RSSI of last packet in list if RSSICnt is greater than 1
	LastSeqNum     uint16       `protobuf:"varint,5"`   // Sequence number of the last RX Probe Request Pkt
	FirstClockTime int64        `protobuf:"zigzag64,7"` // nanosecond unix timestamp of first RX packet in list
	LastClockTime  int64        `protobuf:"zigzag64,8"` // nanosecond unix timestamp of last RX packet in list
}

type ProbeRequestPkt struct {
	MAC        ethernet.MAC `protobuf:"bytes,1"` // client Address
	RSSI       wifi.RSSI    `protobuf:"zigzag32,2"`
	Channel    wifi.Channel `protobuf:"varint,3"`
	LastRxTime time.Time    `protobuf:"bytes,4"` // RX timestamp
	SeqNum     uint16       `protobuf:"varint,5"`
	SSID       string       `protobuf:"bytes,6"`
}

type BLEMotionStat struct {
	CalcLastMvTime []time.Time `protobuf:"bytes,1"` // timestamp of entry
}

type BleAccelData struct {
	Xaxis float32 `protobuf:"fixed32,1"` // x-axis acceleration value
	Yaxis float32 `protobuf:"fixed32,2"` // y-axis acceleration value
	Zaxis float32 `protobuf:"fixed32,3"` // z-axis acceleration value
}

// RSSI measurements of BLE devices the AP can hear
// these appear inbedded in ap-stats- messages, and shuffled or independantly published to ap-ble-rssi-stats- or ap-ble-rssi-stats-zone-
type BleRssiStats struct {
	MAC      ethernet.MAC     `protobuf:"bytes,1"`     // Client MAC
	RandAddr bool             `protobuf:"varint,14"`   // true for random, false for public
	DevName  string           `protobuf:"bytes,3"`     // Client BLE device name
	DevUUID  uuid.UUID        `protobuf:"bytes,4"`     // Client BLE device UUID
	UUID     uuid.UUID        `protobuf:"bytes,5"`     // Client BLE UUID
	Major    uint16           `protobuf:"varint,6"`    // Client BLE Major
	Minor    uint16           `protobuf:"varint,7"`    // Client BLE Minor
	Beam     uint8            `protobuf:"varint,8"`    // beam used to Rx the Client BLE packet (advertisement or scan response)
	TxPower  int8             `protobuf:"zigzag32,19"` // Transmit power of the advertising device
	PktType  uint8            `protobuf:"varint,20"`   // Enumerated values of iBeacon/Eddystone/Altbeacon
	BandPkts []ClientRssiPkts `protobuf:"bytes,9"`     // client rssi info from 2.4GHz BLE

	APBleUUID  uuid.UUID `protobuf:"bytes,10"`  // AP's configured BLE UUID used in advertisements
	APBleMajor uint16    `protobuf:"varint,11"` // AP's configured BLE Major used in advertisements
	APBleMinor uint16    `protobuf:"varint,12"` // AP's configured BLE Minor used in advertisements

	BleMotionRec  BLEMotionStat `protobuf:"bytes,13"`  // Motion statistics such as last moved time
	BleAccelRec   BleAccelData  `protobuf:"bytes,15"`  // Accelerometer values coming from a beacon
	MotionCounter uint8         `protobuf:"varint,18"` // Counter to indicate elapsed time since last motion

	MfgInfo BLEMfgDataFast `protobuf:"bytes,21"` // Mfg Data for LE

	Copied *struct { // the ep-telemetry shuffle's copied values. filled in by ep-terminator
		ID     ethernet.MAC `protobuf:"bytes,1"` // the base MAC (id) of the EP sending these stats
		OrgID  uuid.UUID    `protobuf:"bytes,2"` // the organization-id (who owns the device)
		SiteID uuid.UUID    `protobuf:"bytes,4"` // Site ID configured from the cloud for this AP

		MapID uuid.UUID  `protobuf:"bytes,16"`   // AP location
		XYZ   [3]float64 `protobuf:"fixed64,17"` // relative to the Map's origin

		When time.Time `protobuf:"bytes,5"` // when these stats were gathered

		ExtIO []ExtIOStats `protobuf:"bytes,58" json:",omitempty"` // optional; absent on APs without an external IO connector
	} `protobuf:"bytes,2016" json:",omitempty"`
	InfoFromTerminator *msgs.InfoFromTerminator `protobuf:"bytes,2047" json:",omitempty"`
}

// BLERssiScanStats - a combination of scan and rssi data from BLE
// these appear inbedded in ap-stats- messages, and shuffled or independantly published to ap-ble-rssi-scan-results-
type BLERssiScanStats struct {
	MAC         ethernet.MAC   `protobuf:"bytes,1"` // Client MAC
	RssiStats   BleRssiStats   `protobuf:"bytes,3"`
	ScanResults BLEScanResults `protobuf:"bytes,2"`

	Copied *struct { // the ep-telemetry shuffle's copied values. filled in by ep-terminator
		ID     ethernet.MAC `protobuf:"bytes,1"` // the base MAC (id) of the EP sending these stats
		OrgID  uuid.UUID    `protobuf:"bytes,2"` // the organization-id (who owns the device)
		SiteID uuid.UUID    `protobuf:"bytes,4"` // Site ID configured from the cloud for this AP

		MapID uuid.UUID  `protobuf:"bytes,16"`   // AP location
		XYZ   [3]float64 `protobuf:"fixed64,17"` // relative to the Map's origin

		When time.Time `protobuf:"bytes,5"` // when these stats were gathered
	} `protobuf:"bytes,2016" json:",omitempty"`
	InfoFromTerminator *msgs.InfoFromTerminator `protobuf:"bytes,2047" json:",omitempty"`
}

// RSSI measurements of Wifi devices the AP can hear
// these appear inbedded in ap-stats- messages, and shuffled or independantly published to ap-wifi-rssi-stats-, ap-wifi-rssi-stats-zone- and ap-wifi-rogue-clients
type WifiRssiStats struct {
	MAC      ethernet.MAC     `protobuf:"bytes,1"` // Client MAC
	BandPkts []ClientRssiPkts `protobuf:"bytes,9"` // client rssi info from Wifi 2.4GHz, Wifi 5GHz

	PotentialRogue bool `protobuf:"varint,17"` // potential rogue, as determined by this AP

	Copied *struct { // the ep-telemetry shuffle's copied values. filled in by ep-terminator
		ID     ethernet.MAC `protobuf:"bytes,1"` // the base MAC (id) of the EP sending these stats
		OrgID  uuid.UUID    `protobuf:"bytes,2"` // the organization-id (who owns the device)
		SiteID uuid.UUID    `protobuf:"bytes,4"` // Site ID configured from the cloud for this AP

		MapID uuid.UUID  `protobuf:"bytes,16"`   // AP location
		XYZ   [3]float64 `protobuf:"fixed64,17"` // relative to the Map's origin

		When time.Time `protobuf:"bytes,5"` // when these stats were gathered

		ExtIO []ExtIOStats `protobuf:"bytes,58" json:",omitempty"` // optional; absent on APs without an external IO connector
	} `protobuf:"bytes,2016" json:",omitempty"`
	InfoFromTerminator *msgs.InfoFromTerminator `protobuf:"bytes,2047" json:",omitempty"`
}

// ClientRssiStats is LEGACY, use BleRssiStats or WifiRssiStats in new code
// RSSI measurements of BLE or WIFI devices the AP can hear
type ClientRssiStats struct {
	MAC       ethernet.MAC     `protobuf:"bytes,1"`     // Client MAC
	BleDevice bool             `protobuf:"varint,16"`   // true for BLE; false for WiFi
	RandAddr  bool             `protobuf:"varint,14"`   // true for random, false for public
	DevName   string           `protobuf:"bytes,3"`     // Client BLE device name
	DevUUID   uuid.UUID        `protobuf:"bytes,4"`     // Client BLE device UUID
	UUID      uuid.UUID        `protobuf:"bytes,5"`     // Client BLE UUID
	Major     uint16           `protobuf:"varint,6"`    // Client BLE Major
	Minor     uint16           `protobuf:"varint,7"`    // Client BLE Minor
	Beam      uint8            `protobuf:"varint,8"`    // beam used to Rx the Client BLE packet (advertisement or scan response)
	TxPower   int8             `protobuf:"zigzag32,19"` // Transmit power of the advertising device
	PktType   uint8            `protobuf:"varint,20"`   // Enumerated values of iBeacon/Eddystone/Altbeacon
	BandPkts  []ClientRssiPkts `protobuf:"bytes,9"`     // client rssi info from Wifi 2.4GHz, Wifi 5GHz, or 2.4GHz BLE

	PotentialRogue bool `protobuf:"varint,17"` // potential rogue, as determined by this AP

	APBleUUID  uuid.UUID `protobuf:"bytes,10"`  // AP's configured BLE UUID used in advertisements
	APBleMajor uint16    `protobuf:"varint,11"` // AP's configured BLE Major used in advertisements
	APBleMinor uint16    `protobuf:"varint,12"` // AP's configured BLE Minor used in advertisements

	BleMotionRec  BLEMotionStat `protobuf:"bytes,13"`  // Motion statistics such as last moved time
	BleAccelRec   BleAccelData  `protobuf:"bytes,15"`  // Accelerometer values coming from a beacon
	MotionCounter uint8         `protobuf:"varint,18"` // Counter to indicate elapsed time since last motion

	MfgInfo BLEMfgDataFast `protobuf:"bytes,21"` // Mfg Data for LE

	Copied *struct { // the ep-telemetry shuffle's copied values. filled in by ep-terminator
		ID     ethernet.MAC `protobuf:"bytes,1"` // the base MAC (id) of the EP sending these stats
		OrgID  uuid.UUID    `protobuf:"bytes,2"` // the organization-id (who owns the device)
		SiteID uuid.UUID    `protobuf:"bytes,4"` // Site ID configured from the cloud for this AP

		MapID uuid.UUID  `protobuf:"bytes,16"`   // AP location
		XYZ   [3]float64 `protobuf:"fixed64,17"` // relative to the Map's origin

		When time.Time `protobuf:"bytes,5"` // when these stats were gathered

		ExtIO []ExtIOStats `protobuf:"bytes,58" json:",omitempty"` // optional; absent on APs without an external IO connector
	} `protobuf:"bytes,2016" json:",omitempty"`
	InfoFromTerminator *msgs.InfoFromTerminator `protobuf:"bytes,2047" json:",omitempty"`
}

// per-band Wifi or BLE packet RSSI measurements
type ClientRssiPkts struct {
	Band wifi.Band `protobuf:"varint,1"`

	// RSSI of packets from the client
	BSSID     ethernet.MAC   `protobuf:"bytes,2"`    // BSSID
	RSSISum   int32          `protobuf:"zigzag32,3"` // Sum of the RSSIs of all the packets
	RSSImWSum float64        `protobuf:"fixed64,10"`
	RSSICnt   uint32         `protobuf:"varint,4"` // number of packets
	LastRSSI  PktRSSI        `protobuf:"bytes,5"`  // RSSI and per antenna RSSI of last packet
	Channels  []wifi.Channel `protobuf:"varint,6"` // channels on which packets were heard

	FirstClockTime int64 `protobuf:"zigzag64,7"` // nanosecond unix timestamp of first RX packet in list
	LastClockTime  int64 `protobuf:"zigzag64,8"` // nanosecond unix timestamp of last RX packet in list

	RSSIList []wifi.RSSI `protobuf:"zigzag32,9"` // list of all received RSSI values
}

type AiriqStats struct {
	Type             uint32   `protobuf:"varint,1" json:"type"`
	RSSI             int32    `protobuf:"zigzag32,2" json:"RSSI"`
	Chanutil         uint32   `protobuf:"varint,3" json:"chanutil"`
	Frequency_khz    uint32   `protobuf:"varint,4" json:"frequency_khz"`
	Timestamp        uint32   `protobuf:"fixed32,5" json:"timestamp"`
	TimestampHi      uint32   `protobuf:"fixed32,6" json:"timestampHi"`
	Impactedchannels []uint16 `protobuf:"varint,7" json:"impactedchannels"`
}

// from the perspective of WC, src/dst port, up/down stream, etc.
type AppSortServ struct {
	UserMac ethernet.MAC `protobuf:"bytes,1"`
	App     string       `protobuf:"bytes,9"`
	Site    string       `protobuf:"bytes,2"`
	// protobuf id 3 is reserved
	SiteIp   ip.Addr4    `protobuf:"bytes,10"`
	Protocol ip.Protocol `protobuf:"varint,4"`
	Sport    uint16      `protobuf:"varint,5"`
	Dport    uint16      `protobuf:"varint,6"`
	Up       uint32      `protobuf:"varint,7"` // upstream flow in byte
	Down     uint32      `protobuf:"varint,8"` // downstream flow in byte
	SSID     string      `protobuf:"bytes,11"`
	WlanId   uuid.UUID   `protobuf:"bytes,12"`
}

type WxlanUsage struct {
	UserMac ethernet.MAC `protobuf:"bytes,1"`  // The MAC of the WC
	RuleId  uuid.UUID    `protobuf:"bytes,2"`  // The wxlan rule id from database
	TagId   uuid.UUID    `protobuf:"bytes,3"`  // The resource tag id from database
	Usage   uint32       `protobuf:"varint,4"` // number of new flows for this MAC matching this rule id
	AppId   string       `protobuf:"bytes,5" json:",omitempty"`
}

type WxlanFlowState struct {
	State     wxlan.FLOW_STATE `protobuf:"varint,1"` // state of this flow
	Sip       ip.Addr6         `protobuf:"bytes,2"`  // Source IP address
	Dip       ip.Addr6         `protobuf:"bytes,3"`  // Dest IP address
	Eth_proto uint16           `protobuf:"varint,4"` // Ethernet Protocol
	Vlan      uint16           `protobuf:"varint,5"` // Vlan Tag
	Sport     uint16           `protobuf:"varint,6"` // Source IP port.
	Dport     uint16           `protobuf:"varint,7"` // Destination IP port.
	Ip_proto  ip.Protocol      `protobuf:"varint,8"` // IP Protocol.
	Src_dev   string           `protobuf:"bytes,9"`  // Src Device name.
	Dmac      ethernet.MAC     `protobuf:"bytes,10"` // Dest MAC address
	Smac      ethernet.MAC     `protobuf:"bytes,11"` // Source MAC address
	When      time.Time        `protobuf:"bytes,12"` // when this flow is allowed/denied/timedout, etc.
	Hostname  string           `protobuf:"bytes,13" json:",omitempty"`
}

// find the ClientStats of the given client (by MAC). returns nil if client isn't found
func (st *APStats) FindClient(mac ethernet.MAC) *ClientStats {
	// st.Clients is not sorted. it is also short (<250 max, and very often <10) so a linear search is faster than building up a fancy cache
	for i := range st.Clients {
		if st.Clients[i].MAC == mac {
			return &st.Clients[i]
		}
	}
	return nil
}

// AP Environment related data like Orientation, Temperature, Humidity and Pressure
type Environment struct {
	Orientation       Orientation `protobuf:"bytes,1"`
	Temperature       Temperature `protobuf:"bytes,2"`
	Relative_Humidity float32     `protobuf:"fixed32,3"` // Relative Humidity in percentage
	Pressure          float32     `protobuf:"fixed32,4"` // Pressure in hPa
}

// AP orientation relative to gravity (assuming the AP isn't vibrating), and the local magnetic field
type Orientation struct {
	// axis are chosen to match the AP when it is sitting on its back in front of you
	//  X is to the right. Imagine a vector from the 'M' through the 't' in 'Mist'.
	//  Y is to the back. Imagine a vector from the 'i' in 'Mist' through the LED.
	//  Z is up. Imaging a vector up from the LED into the air above it.
	Accel struct {
		X float32 `protobuf:"fixed32,1"` // acceleration (of gravity, if the AP is well mounted) in unit of 1 'g' (9.8 m/s^2)
		Y float32 `protobuf:"fixed32,2"`
		Z float32 `protobuf:"fixed32,3"`
	} `json:",omitempty" protobuf:"bytes,1"`
	Magne struct {
		X float32 `protobuf:"fixed32,1"` // local magnetic field in units of uT
		Y float32 `protobuf:"fixed32,2"`
		Z float32 `protobuf:"fixed32,3"`
	} `json:",omitempty" protobuf:"bytes,2"`
}

// temperatures as measured by an AP.
// some or any of these might be omitted, in which case they don't exist
type Temperature struct {
	Ambient  float32 `json:",omitempty" protobuf:"fixed32,1"` // ambient (or as closely as this device can from within its case) air temp in deg-C
	CPU      float32 `json:",omitempty" protobuf:"fixed32,2"` // CPU die temperature in deg-C
	Attitude float32 `json:",omitempty" protobuf:"fixed32,3"` // Attitude sensor die temperature in deg-C
}

// state of the cloud connection
type CloudStats struct {
	LastRTT     float32   `protobuf:"fixed32,1"` // last RTT measured between AP and cloud, in seconds (hopefully much less than one second). If 0 then it isn't known.
	WhenLastRTT time.Time `protobuf:"bytes,2"`   // when LastRTT was measured
	PMTU        uint32    `protobuf:"varint,3"`  // path-MTU discovered by TCP connection
	SendMSS     uint32    `protobuf:"varint,4"`  // MSS of TCP connection when sending. When the path includes TCP MSS clamping it happens that the PMTU is never discovered, byt the clamped MSS tells the story
	// other things in here in the future, if interesting, could be byte/packet counters, and the # of connections since reset
}

type MeshRemoteStats struct {
	BSSID           ethernet.MAC  `protobuf:"bytes,1"`    // all zeros if disconneted
	MAC             ethernet.MAC  `protobuf:"bytes,2"`    // all zeros if disconnected
	Dev             string        `protobuf:"bytes,3"`    // name of the interface on the remote
	LastConnectTime time.Time     `protobuf:"bytes,4"`    // time connected/reconnected to this bssid
	RSSI            wifi.RSSI     `protobuf:"zigzag32,5"` // current rssi
	Channel         wifi.Channel  `protobuf:"varint,6"`   // channel of the base AP
	Stats           []ClientStats `protobuf:"bytes,7"`    // remote stats (array of one with connected to base) otherwise nil
}

// LLDP Power Negotiation stats.
type LLDPpwrStats struct {
	Req_cnt   uint32 `protobuf:"varint,1"` // Count of the number of negotiation requests
	Xtnd_sup  bool   `protobuf:"varint,2"` // Extended power Negotiation Supported.
	Pse_avail uint16 `protobuf:"varint,3"` // Power Avail from PSE  (mW)
	Pd_req    uint16 `protobuf:"varint,4"` // Power last requested by PD (mW)
	Pd_needs  uint16 `protobuf:"varint,5"` // Power Needed by PD (mW)
}

type LLDPvlanNm struct {
	Vlan_id   uint16 `protobuf:"varint,1"` // Vlan ID
	Vlan_name string `protobuf:"bytes,2"`  // Vlan Name String
}

// LLDP stats and Negotiated Power Levels.
type LLDPneighbor struct {
	Port_desc     string       `protobuf:"bytes,1"`  // Attached Port Description
	Sys_name      string       `protobuf:"bytes,2"`  // System name
	Sys_desc      string       `protobuf:"bytes,3"`  // System Description
	Mgmt_addr     string       `protobuf:"bytes,4"`  // Management Address
	Port_id       string       `protobuf:"bytes,5"`  // Port id
	Autoneg_sup   string       `protobuf:"bytes,6"`  // Auto Negotiation supported / Enabled.
	Autoneg_adv   string       `protobuf:"bytes,7"`  // Auto Negotiation speed and duplex advertised
	MTU           uint16       `protobuf:"varint,8"` // The Maximum Frame Size
	PortVlan_id   uint16       `protobuf:"varint,9"` // The Port Vlan ID
	PortVlan_name []LLDPvlanNm `protobuf:"bytes,10"` // The Port Vlan Name String
	Hardware_rev  string       `protobuf:"bytes,11"` // The Switch Hardware Rev.
	Fw_rev        string       `protobuf:"bytes,12"` // The Switch Firmware Rev.
	Sw_rev        string       `protobuf:"bytes,13"` // The Switch Software Rev.
	Serial_number string       `protobuf:"bytes,14"` // The Switch Serial Number.
	Mfg_name      string       `protobuf:"bytes,15"` // The Switch Manufacturer Name.
	Model_name    string       `protobuf:"bytes,16"` // The Switch Model Name (LLDP-MED)
	Asset_id      string       `protobuf:"bytes,17"` // The Switch Asset ID
	Chassis_id    string       `protobuf:"bytes,18"` // The Switch Chassis ID
}

// AP Power Manager stats.
type PMGRStats struct {
	Pwr_src    string `protobuf:"bytes,1"`  // Power source Description
	Pwr_avail  uint32 `protobuf:"varint,2"` // Total Power Avail at AP from pwr source
	Pwr_needed uint32 `protobuf:"varint,3"` // Total Power needed incl Peripherals
	// 4 is reserved; was the Pwr_budget as a varint
	Pwr_budget int32 `protobuf:"zigzag32,5"` // Amount of power headroom with enabled features
}

// External IO pin stats. One per port configured in the config
type ExtIOStats struct {
	ID     string `protobuf:"bytes,1"`                   // nominal name of pin. These match what is printed on the blue ExtIO press-fit connectors ("DI1", "DI2", "DO", "A1", ...)
	Name   string `protobuf:"bytes,2" json:",omitempty"` // free-form customer name for the pin, copied from the config.ExtIOConfig.Name
	Value  uint8  `protobuf:"varint,3"`                  // current digital value of pin (0 or 1)
	Analog uint8  `protobuf:"varint,4"`                  // current raw analog value (applicable only to pins A1...A4)
	Pulses uint32 `protobuf:"varint,5"`                  // # of times the pin has pulsed (low->high edges) since bootup. note the counter wraps at 4B, and the maximum reliable freq is a few kHz

	// triggers are not yet implemented
	//Triggers uint   `protobuf:"varint,6"`                  // # of times the pin has triggered since we booted
}
