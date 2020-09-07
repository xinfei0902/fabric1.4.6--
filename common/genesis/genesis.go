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

package genesis

import (
	cb "github.com/hyperledger/fabric/protos/common"
	"github.com/hyperledger/fabric/protos/utils"
)

const (
	msgVersion = int32(1)

	// These values are fixed for the genesis block.
	epoch = 0
)

// Factory facilitates the creation of genesis blocks.
type Factory interface {
	// Block returns a genesis block for a given channel ID.
	Block(channelID string) *cb.Block
}

type factory struct {
	channelGroup *cb.ConfigGroup
}

// NewFactoryImpl creates a new Factory.
func NewFactoryImpl(channelGroup *cb.ConfigGroup) Factory {
	return &factory{channelGroup: channelGroup}
}

// Block constructs and returns a genesis block for a given channel ID.
func (f *factory) Block(channelID string) *cb.Block {
	payloadChannelHeader := utils.MakeChannelHeader(cb.HeaderType_CONFIG, msgVersion, channelID, epoch)                                             //构建块里的payload中 channel_header{}对象信息
	payloadSignatureHeader := utils.MakeSignatureHeader(nil, utils.CreateNonceOrPanic())                                                            //构建和channel_header同级signatur_header{}对象
	utils.SetTxID(payloadChannelHeader, payloadSignatureHeader)                                                                                     //随机24字节和签署者做hash 生成txid 赋值与channel_header{} tx_id
	payloadHeader := utils.MakePayloadHeader(payloadChannelHeader, payloadSignatureHeader)                                                          //把上述 channel_header signatur_header 套一层header结构
	payload := &cb.Payload{Header: payloadHeader, Data: utils.MarshalOrPanic(&cb.ConfigEnvelope{Config: &cb.Config{ChannelGroup: f.channelGroup}})} //做一个payload
	envelope := &cb.Envelope{Payload: utils.MarshalOrPanic(payload), Signature: nil}                                                                //然后做成信封

	block := cb.NewBlock(0, nil)                                               //构建初始块
	block.Data = &cb.BlockData{Data: [][]byte{utils.MarshalOrPanic(envelope)}} //赋值
	block.Header.DataHash = block.Data.Hash()                                  //数据做hash
	block.Metadata.Metadata[cb.BlockMetadataIndex_LAST_CONFIG] = utils.MarshalOrPanic(&cb.Metadata{
		Value: utils.MarshalOrPanic(&cb.LastConfig{Index: 0}),
	})
	return block
}
