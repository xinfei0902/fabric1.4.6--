/*
Copyright IBM Corp. 2017 All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

                 http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package fileledger

import (
	"sync"

	"github.com/hyperledger/fabric/common/ledger/blkstorage"
	"github.com/hyperledger/fabric/common/ledger/blkstorage/fsblkstorage"
	"github.com/hyperledger/fabric/common/ledger/blockledger"
	"github.com/hyperledger/fabric/common/metrics"
)

type fileLedgerFactory struct {
	blkstorageProvider blkstorage.BlockStoreProvider
	ledgers            map[string]blockledger.ReadWriter
	mutex              sync.Mutex
}

// GetOrCreate gets an existing ledger (if it exists) or creates it if it does not
func (flf *fileLedgerFactory) GetOrCreate(chainID string) (blockledger.ReadWriter, error) {
	flf.mutex.Lock()
	defer flf.mutex.Unlock()

	key := chainID
	// check cache
	ledger, ok := flf.ledgers[key] //检测是否存在 一般刚开始构建区块网络是不存在
	if ok {
		return ledger, nil
	}
	// open fresh
	blockStore, err := flf.blkstorageProvider.OpenBlockStore(key) //实现 最外层 common/ledger/blkstorage/fsblkstorage/fs_blockstore_provider.go No.50 line
	if err != nil {
		return nil, err
	}
	ledger = NewFileLedger(blockStore)
	flf.ledgers[key] = ledger
	return ledger, nil
}

// ChainIDs returns the chain IDs the factory is aware of
func (flf *fileLedgerFactory) ChainIDs() []string {
	chainIDs, err := flf.blkstorageProvider.List()
	if err != nil {
		logger.Panic(err)
	}
	return chainIDs
}

// Close releases all resources acquired by the factory
func (flf *fileLedgerFactory) Close() {
	flf.blkstorageProvider.Close()
}

// New creates a new ledger factory
func New(directory string, metricsProvider metrics.Provider) blockledger.Factory {
	return &fileLedgerFactory{
		blkstorageProvider: fsblkstorage.NewProvider(
			fsblkstorage.NewConf(directory, -1), //设置块存储地址 以及存储最大大小
			&blkstorage.IndexConfig{ //索引配置
				AttrsToIndex: []blkstorage.IndexableAttr{blkstorage.IndexableAttrBlockNum}},
			metricsProvider,
		),
		ledgers: make(map[string]blockledger.ReadWriter),
	}
}
