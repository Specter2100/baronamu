package main

// 노드와 연결-블록 받기-검증-시스템 종료료
import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"

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
	dataDirFlag := flag.String("datadir", "", "Directory to store data")
	flag.Parse()

	// 데이터 디렉토리 기본값 설정
	var dataDir string
	if *dataDirFlag == "" {
		if runtime.GOOS == "windows" {
			dataDir = "E:\\Bit\\활동\\코딩\\git\\utreexod\\cmd\\chaintipval"
		} else {
			//dataDir = filepath.Join(os.Getenv("HOME"), ".utreexod")
			panic("")
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
		UtreexoView: nil,
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
			err = requestBlocks(conn, netParams, chain)
			if err != nil {
				log.Fatalf("Failed during block request: %v", err)
			}
			return
		default:
			fmt.Printf("Received other message: %T\n", m)
		}
	}
}

func requestBlocks(conn net.Conn, netParams *chaincfg.Params, chain *blockchain.BlockChain) error {
	genesisHash := netParams.GenesisHash
	targetBlockHash, err := chainhash.NewHashFromStr("00000109740b87e36d6092cbc1e92bdc17f92b52ad225b6dcdd62ca8ab0820d1")
	if err != nil {
		return fmt.Errorf("invalid target block hash: %v", err)
	}

	currentHeight := chain.BestSnapshot().Height
	fmt.Println("currentHeight", currentHeight)
	var blockLocator []*chainhash.Hash
	if currentHeight == 0 {
		blockLocator = chain.BlockLocatorFromHash(genesisHash)
	} else {
		blockLocator = chain.BlockLocatorFromHash(&chain.BestSnapshot().Hash)
	}

	// getheaders 요청
	getHeadersMsg := &wire.MsgGetHeaders{
		ProtocolVersion:    wire.ProtocolVersion,
		BlockLocatorHashes: blockLocator,
		HashStop:           *targetBlockHash,
	}
	err = wire.WriteMessage(conn, getHeadersMsg, 0, netParams.Net)
	if err != nil {
		return fmt.Errorf("failed to send getheaders message: %v", err)
	}
	fmt.Println("Sent getheaders request to check peer height up to target")

	// getblocks 요청
	getBlocksMsg := &wire.MsgGetBlocks{
		ProtocolVersion:    wire.ProtocolVersion,
		BlockLocatorHashes: blockLocator,
		HashStop:           *targetBlockHash,
	}
	err = wire.WriteMessage(conn, getBlocksMsg, 0, netParams.Net)
	if err != nil {
		return fmt.Errorf("failed to send getblocks message: %v", err)
	}
	fmt.Println("Sent initial getblocks request")
	fmt.Println("chain best height", chain.BestSnapshot().Height)

	blocksInQueue := make(map[chainhash.Hash]struct{})

	for {
		fmt.Println("Waiting for message...")
		msg, _, err := wire.ReadMessage(conn, 0, netParams.Net)
		if err != nil {
			log.Printf("Failed to read message: %v", err)
			continue
		}
		fmt.Printf("Received message: %T\n", msg)

		switch m := msg.(type) {
		case *wire.MsgHeaders:
			headerCount := len(m.Headers)
			if headerCount > 0 {
				peerHeight := currentHeight + int32(headerCount)
				fmt.Printf("Peer reported %d headers up to target, estimated height: %d\n", headerCount, peerHeight)
				// 수정: 포인터 메서드 호출 문제 해결
				for _, header := range m.Headers {
					headerHash := header.BlockHash()         // *chainhash.Hash
					if headerHash.IsEqual(targetBlockHash) { // 둘 다 *Hash로 비교
						fmt.Println("Target block found in peer headers")
						break
					}
				}
			} else {
				fmt.Println("Peer returned no headers up to target")
			}

		case *wire.MsgInv:
			fmt.Printf("MsgInv with %d items\n", len(m.InvList))
			getDataMsg := wire.NewMsgGetData()
			for i, inv := range m.InvList {
				fmt.Printf(" - Item %d: %s\n", i, inv.Hash.String())
				if inv.Type == wire.InvTypeBlock {
					getDataMsg.AddInvVect(inv)
					blocksInQueue[inv.Hash] = struct{}{}
					fmt.Printf("Requested block: %s\n", inv.Hash.String())
				}
			}
			if len(getDataMsg.InvList) == 0 {
				fmt.Println("Empty InvList, requesting more blocks")
				blockLocator = chain.BlockLocatorFromHash(&chain.BestSnapshot().Hash)
				getBlocksMsg = &wire.MsgGetBlocks{
					ProtocolVersion:    wire.ProtocolVersion,
					BlockLocatorHashes: blockLocator,
					HashStop:           *targetBlockHash,
				}
				err = wire.WriteMessage(conn, getBlocksMsg, 0, netParams.Net)
				if err != nil {
					return fmt.Errorf("failed to send additional getblocks: %v", err)
				}
				fmt.Println("Sent additional getblocks request")
				continue
			}
			fmt.Printf("Sending getdata for %d blocks\n", len(getDataMsg.InvList))
			err = wire.WriteMessage(conn, getDataMsg, 0, netParams.Net)
			if err != nil {
				return fmt.Errorf("failed to send getdata message: %v", err)
			}
			fmt.Println("Sent getdata request")
			fmt.Println("Current blocks in queue:", len(blocksInQueue))
			for hash := range blocksInQueue {
				fmt.Printf(" - Queued block: %s\n", hash.String())
			}

		case *wire.MsgBlock:
			block := btcutil.NewBlock(m)
			delete(blocksInQueue, *block.Hash())
			snapshot := chain.BestSnapshot()
			fmt.Printf("best height %v, hash %v, got block %v\n",
				snapshot.Height, snapshot.Hash, block.Hash())
			isMainChain, _, err := chain.ProcessBlock(block, blockchain.BFNone)
			if !isMainChain {
				fmt.Printf("Received orphan block: %s, %v\n", block.Hash().String(), err)
				continue
			}
			if err != nil {
				fmt.Printf("block validation failed for %s: %v\n", block.Hash().String(), err)
				continue
			}
			if targetBlockHash.IsEqual(block.Hash()) {
				fmt.Println("Target block reached, exiting")
				conn.Close()
				os.Exit(0)
			}

			fmt.Println("chain best height", chain.BestSnapshot().Height)
			fmt.Println("blocks in queue", len(blocksInQueue))

			if len(blocksInQueue) == 0 {
				fmt.Println("All requested blocks received")
				haveTarget, err := chain.HaveBlock(targetBlockHash)
				if err != nil {
					log.Printf("Error checking target block existence: %v", err)
					continue
				}
				if !haveTarget {
					fmt.Println("Target not reached, sending new getblocks")
					blockLocator = blockchain.BlockLocator([]*chainhash.Hash{block.Hash()})
					fmt.Printf("locator %v, target %v\n", block.Hash(), targetBlockHash)
					getBlocksMsg = &wire.MsgGetBlocks{
						ProtocolVersion:    wire.ProtocolVersion,
						BlockLocatorHashes: blockLocator,
						HashStop:           *targetBlockHash,
					}
					err = wire.WriteMessage(conn, getBlocksMsg, 0, netParams.Net)
					if err != nil {
						return fmt.Errorf("failed to send next getblocks: %v", err)
					}
					fmt.Println("Sent additional getblocks request")
				}
			}

		case *wire.MsgReject:
			fmt.Printf("Reject: %s\n", m.Reason)
			return fmt.Errorf("rejected: %s", m.Reason)

		default:
			fmt.Printf("Other message: %T\n", m)
		}
	}
}
