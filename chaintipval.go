package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"

	"github.com/utreexo/utreexod/blockchain"
	"github.com/utreexo/utreexod/btcutil"
	"github.com/utreexo/utreexod/chaincfg"
	"github.com/utreexo/utreexod/chaincfg/chainhash"
	"github.com/utreexo/utreexod/database"
	"github.com/utreexo/utreexod/wire"
)

func main() {
	signet := flag.Bool("signet", false, "Enable Signet network")
	testnet3 := flag.Bool("testnet3", false, "Enable Testnet3 network")
	connect := flag.String("connect", "", "IP")
	dataDir := flag.String("datadir", btcutil.AppDataDir("utreexod", false), "Directory to store data")

	flag.Parse()

	if *signet && *testnet3 {
		fmt.Println("Error: --signet and --testnet3 cannot be used together.")
		os.Exit(1)
	}

	if *connect == "" {
		fmt.Println("Error: --connect flag is required.")
		fmt.Println("Usage: --connect <IP address>")
		os.Exit(1)
	}

	var netParams *chaincfg.Params
	var defaultPort string
	switch {
	case *signet:
		netParams = &chaincfg.SigNetParams
		defaultPort = "38333"
		fmt.Println("Using Signet network")
	case *testnet3:
		netParams = &chaincfg.TestNet3Params
		defaultPort = "18333"
		fmt.Println("Using Testnet3 network")
	default:
		fmt.Println("Error: Please specify --signet or --testnet3")
		os.Exit(1)
	}
	host, port, err := net.SplitHostPort(*connect)
	if err != nil {
		host = *connect
		port = defaultPort
	}

	ipAddr := net.ParseIP(host)
	if ipAddr == nil {
		fmt.Println("Wrong IP Address.Try again")
		os.Exit(1)
	}

	fullAddress := fmt.Sprintf("%s:%s", host, port)
	fmt.Printf("Connecting to node: %s\n", fullAddress)

	// 데이터베이스 초기화
	// 데이터베이스 경로 설정
	// 데이터베이스 경로 설정
	dbPath := filepath.Join(*dataDir, "blocks_ffldb")

	// 데이터베이스 열기 (올바른 인자 전달)
	// 데이터베이스 열기 (ffldb가 아니라 database.Open 사용)
	db, err := database.Open("ffldb", dbPath, netParams.Net) //여기가 막힘힘
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// BlockChain 초기화
	chain, err := blockchain.New(&blockchain.Config{
		DB:          db,
		ChainParams: netParams,
		TimeSource:  blockchain.NewMedianTime(),
		UtreexoView: &blockchain.UtreexoViewpoint{}, // UtreexoViewpoint 설정
		Checkpoints: netParams.Checkpoints,
		Interrupt:   nil, // 인터럽트 채널은 필요 시 추가
	})
	if err != nil {
		log.Fatalf("Failed to create blockchain: %v", err)
	}

	connectToNode(fullAddress, netParams, chain)
}

func connectToNode(nodeIP string, netParams *chaincfg.Params, chain *blockchain.BlockChain) {
	conn, err := net.Dial("tcp", nodeIP)
	if err != nil {
		log.Fatalf("Failed to connect to node: %v", err)
	}
	defer conn.Close()
	fmt.Println("Connected to node:", nodeIP)

	localAddr := conn.LocalAddr().(*net.TCPAddr)
	remoteAddr := conn.RemoteAddr().(*net.TCPAddr)
	var serviceFlag wire.ServiceFlag
	serviceFlag = wire.SFNodeNetworkLimited

	verMsg := wire.NewMsgVersion(
		wire.NewNetAddressIPPort(localAddr.IP, uint16(localAddr.Port), serviceFlag),
		wire.NewNetAddressIPPort(remoteAddr.IP, uint16(remoteAddr.Port), 0),
		0,
		0,
	)

	err = wire.WriteMessage(conn, verMsg, 0, netParams.Net)
	if err != nil {
		log.Fatalf("Failed to send version message: %v", err)
	}
	fmt.Println("Sent version message")

	for {
		msg, _, err := wire.ReadMessage(conn, 0, netParams.Net)
		if err != nil {
			log.Fatalf("Failed to read message: %v", err)
		}

		switch m := msg.(type) {
		case *wire.MsgVerAck:
			fmt.Println("Received verack")
			verAckMsg := wire.NewMsgVerAck()
			err = wire.WriteMessage(conn, verAckMsg, 0, netParams.Net)
			if err != nil {
				log.Fatalf("Failed to send verack message: %v", err)
			}
			fmt.Println("Sent verack response")
			requestBlocks(conn, netParams, chain)
			return
		default:
			fmt.Printf("Received other message: %T\n", m)
		}
	}
}

func requestBlocks(conn net.Conn, netParams *chaincfg.Params, chain *blockchain.BlockChain) {
	genesisHash := netParams.GenesisHash
	getBlocksMsg := &wire.MsgGetBlocks{
		ProtocolVersion:    wire.ProtocolVersion,
		BlockLocatorHashes: []*chainhash.Hash{genesisHash},
		HashStop:           chainhash.Hash{},
	}

	err := wire.WriteMessage(conn, getBlocksMsg, 0, netParams.Net)
	if err != nil {
		log.Fatalf("Failed to send getblocks message: %v", err)
	}
	fmt.Println("Sent getblocks request")

	requested := false

	for {
		msg, _, err := wire.ReadMessage(conn, 0, netParams.Net)
		if err != nil {
			if me, ok := err.(*wire.MessageError); ok && me.Description == "payload exceeds max length" {
				fmt.Println("Received invalid message (size limit exceeded), skipping:", err)
				return
			}
			log.Printf("Failed to read message: %v", err)
			continue
		}

		switch m := msg.(type) {
		case *wire.MsgInv:
			if requested {
				fmt.Println("Ignoring additional MsgInv while waiting for MsgBlock")
				os.Exit(0)
			}
			fmt.Printf("Received inventory message: %d blocks available\n", len(m.InvList))
			getDataMsg := wire.NewMsgGetData()
			for _, inv := range m.InvList {
				if inv.Type == wire.InvTypeBlock {
					getDataMsg.AddInvVect(inv)
				}
			}
			err = wire.WriteMessage(conn, getDataMsg, 0, netParams.Net)
			if err != nil {
				log.Printf("Failed to send getdata message: %v", err)
				return
			}
			fmt.Println("Sent getdata request for all blocks")
			requested = true
		case *wire.MsgBlock:
			fmt.Printf("Received block: %s, TxCount: %d\n",
				m.BlockHash().String(), len(m.Transactions))
			processBlock(m, chain)
			requested = false
		case *wire.MsgReject:
			fmt.Printf("Received reject message: Command=%s, Code=%d, Reason=%s\n",
				m.Cmd, m.Code, m.Reason)
			return
		default:
			fmt.Printf("Received other message: %T\n", m)
		}
	}
}

func processBlock(block *wire.MsgBlock, chain *blockchain.BlockChain) {
	fmt.Println("Processing block:", block.BlockHash().String())
	btcBlock := btcutil.NewBlock(block)
	_, isOrphan, err := chain.ProcessBlock(btcBlock, blockchain.BFNone)
	if err != nil {
		log.Printf("Failed to process block: %v", err)
		return
	}
	if isOrphan {
		fmt.Println("Received an orphan block")
	} else {
		fmt.Println("Block processed successfully")
	}
}
