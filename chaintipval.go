package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"

	"github.com/utreexo/utreexod/blockchain"
	"github.com/utreexo/utreexod/chaincfg"
	"github.com/utreexo/utreexod/chaincfg/chainhash"
	"github.com/utreexo/utreexod/database"
	"github.com/utreexo/utreexod/wire"
)

func main() {
	signet := flag.Bool("signet", false, "Enable Signet network")
	testnet3 := flag.Bool("testnet3", false, "Enable Testnet3 network")
	connect := flag.String("connect", "", "IP")
	dataDirFlag := flag.String("datadir", "", "Directory to store data")
	flag.Parse()

	// 데이터 디렉토리 기본값 설정
	var dataDir string
	if *dataDirFlag == "" {
		if runtime.GOOS == "windows" {
			dataDir = "E:\\Bit\\활동\\코딩\\git\\utreexod\\cmd\\chaintipval"
		} else {
			dataDir = filepath.Join(os.Getenv("HOME"), ".utreexod")
		}
	} else {
		dataDir = *dataDirFlag
	}

	// 네트워크 선택 확인
	if *signet && *testnet3 {
		log.Fatal("Error: --signet and --testnet3 cannot be used together.")
	}
	if *connect == "" {
		log.Fatal("Error: --connect flag is required.\nUsage: --connect <IP address>")
	}

	// 네트워크 설정
	var netParams *chaincfg.Params
	var defaultPort string
	switch {
	case *signet:
		netParams = &chaincfg.SigNetParams
		defaultPort = "38333"
	case *testnet3:
		netParams = &chaincfg.TestNet3Params
		defaultPort = "18333"
	default:
		log.Fatal("Error: Please specify --signet or --testnet3")
	}

	// IP 및 포트 설정
	host, port, err := net.SplitHostPort(*connect)
	if err != nil {
		host = *connect
		port = defaultPort
	}

	if net.ParseIP(host) == nil {
		log.Fatal("Error: Invalid IP address.")
	}

	fullAddress := fmt.Sprintf("%s:%s", host, port)
	fmt.Printf("Connecting to node: %s\n", fullAddress)

	// 데이터베이스 경로 설정
	dbPath := filepath.Join(dataDir, "blocks_ffldb")

	// 데이터베이스 없으면 생성
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Println("Database not found. Creating new database...")
		db, err := database.Create("ffldb", dbPath, netParams.Net)
		if err != nil {
			log.Fatalf("Failed to create database: %v", err)
		}
		db.Close()
	}

	// 데이터베이스 열기
	db, err := database.Open("ffldb", dbPath, netParams.Net)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Blockchain 초기화
	chain, err := blockchain.New(&blockchain.Config{
		DB:          db,
		ChainParams: netParams,
		TimeSource:  blockchain.NewMedianTime(),
		UtreexoView: nil, //일반 노드 수신하고 나중에 UTREEXO 받게
		Checkpoints: netParams.Checkpoints,
		Interrupt:   nil,
	})
	if err != nil {
		log.Fatalf("Failed to create blockchain: %v", err)
	}
	log.Println("Blockchain initialized successfully!")

	// 노드 연결
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

	verMsg := wire.NewMsgVersion(
		wire.NewNetAddressIPPort(localAddr.IP, uint16(localAddr.Port), wire.SFNodeNetworkLimited),
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
			err = wire.WriteMessage(conn, wire.NewMsgVerAck(), 0, netParams.Net)
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
	targetBlockHash, err := chainhash.NewHashFromStr("00000131de56604f752c0b072f468a2904e5d807e7ee79bd32a5be00bef17b2e")
	if err != nil {
		log.Fatalf("Invalid target block hash: %v", err)
	}

	blockLocator := chain.BlockLocatorFromHash(genesisHash)

	getBlocksMsg := &wire.MsgGetBlocks{
		ProtocolVersion:    wire.ProtocolVersion,
		BlockLocatorHashes: blockLocator,
		HashStop:           *targetBlockHash,
	}

	err = wire.WriteMessage(conn, getBlocksMsg, 0, netParams.Net)
	if err != nil {
		log.Fatalf("Failed to send getblocks message: %v", err)
	}
	fmt.Println("Sent getblocks request up to target block")

	for {
		msg, _, err := wire.ReadMessage(conn, 0, netParams.Net)
		if err != nil {
			log.Printf("Failed to read message: %v", err)
			continue
		}

		switch m := msg.(type) {
		case *wire.MsgInv:
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
			fmt.Println("Sent getdata request")

		case *wire.MsgBlock:
			blockHash := m.BlockHash()
			fmt.Printf("Received block: %s, TxCount: %d\n", blockHash.String(), len(m.Transactions))

			// 이 코드 덕에 목표 블록이면 종료 해야하는데???
			if targetBlockHash.IsEqual(&blockHash) {
				fmt.Println("Target block received, stopping download and closing connection.")
				conn.Close() // 연결 종료
				os.Exit(0)   // 프로그램 종료
			}

		case *wire.MsgReject:
			fmt.Printf("Received reject message: Command=%s, Code=%d, Reason=%s\n", m.Cmd, m.Code, m.Reason)
			return

		default:
			fmt.Printf("Received other message: %T\n", m)
		}
	}
}
