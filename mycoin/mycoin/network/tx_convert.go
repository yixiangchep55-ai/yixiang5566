package network

import (
	"math/big"

	"mycoin/blockchain"
)

// Transaction → DTO
func TxToDTO(tx blockchain.Transaction) TransactionDTO {
	outs := make([]TxOutDTO, 0, len(tx.Outputs))
	for _, o := range tx.Outputs {
		outs = append(outs, TxOutDTO{
			Value: big.NewInt(int64(o.Amount)).String(),
			To:    o.To,
		})
	}

	ins := make([]TxInDTO, 0, len(tx.Inputs))
	for _, in := range tx.Inputs {
		ins = append(ins, TxInDTO{
			TxID:   in.TxID,
			Index:  in.Index,
			Sig:    in.Sig,
			PubKey: in.PubKey,
		})
	}

	return TransactionDTO{
		ID:         tx.ID,
		Inputs:     ins,
		Outputs:    outs,
		IsCoinbase: tx.IsCoinbase,
	}
}

func DTOToTx(d TransactionDTO) blockchain.Transaction {
	outs := make([]blockchain.TxOutput, 0, len(d.Outputs))
	for _, o := range d.Outputs {
		v := new(big.Int)
		v.SetString(o.Value, 10)

		outs = append(outs, blockchain.TxOutput{
			Amount: int(v.Int64()),
			To:     o.To,
		})
	}

	ins := make([]blockchain.TxInput, 0, len(d.Inputs))
	for _, in := range d.Inputs {
		ins = append(ins, blockchain.TxInput{
			TxID:   in.TxID,
			Index:  in.Index,
			Sig:    in.Sig,
			PubKey: in.PubKey,
		})
	}

	return blockchain.Transaction{
		ID:         d.ID,
		Inputs:     ins,
		Outputs:    outs,
		IsCoinbase: d.IsCoinbase,
	}
}

func TxListToDTO(txs []blockchain.Transaction) []TransactionDTO {
	list := make([]TransactionDTO, 0, len(txs))
	for _, tx := range txs {
		list = append(list, TxToDTO(tx))
	}
	return list
}

func TxListFromDTO(dtos []TransactionDTO) []blockchain.Transaction {
	list := make([]blockchain.Transaction, 0, len(dtos))
	for _, d := range dtos {
		list = append(list, DTOToTx(d))
	}
	return list
}
