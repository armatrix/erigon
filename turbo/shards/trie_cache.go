package shards

import (
	"bytes"
	"fmt"
	"math/bits"
	"unsafe"

	"github.com/google/btree"
	"github.com/ledgerwatch/turbo-geth/common"
	"github.com/ledgerwatch/turbo-geth/common/dbutils"
	"github.com/ledgerwatch/turbo-geth/core/types/accounts"
	"github.com/ledgerwatch/turbo-geth/ethdb"
)

// An optional addition to the state cache, helping to calculate state root

// Sizes of B-tree items for the purposes of keeping track of the size of reads and writes
// The sizes of the nodes of the B-tree are not accounted for, because their are private to the `btree` package
const (
	accountHashItemSize      = int(unsafe.Sizeof(AccountTrieItem{}) + 16)
	accountHashWriteItemSize = int(unsafe.Sizeof(AccountTrieWriteItem{}) + 16)
	storageHashItemSize      = int(unsafe.Sizeof(StorageTrieItem{}) + 16)
	storageHashWriteItemSize = int(unsafe.Sizeof(StorageTrieWriteItem{}) + 16)
)

type AccountTrieWriteItem struct {
	ai *AccountTrieItem
}

type AccountTrieItem struct {
	sequence                   int
	queuePos                   int
	flags                      uint16
	hasState, hasTree, hasHash uint16
	loadedFromDB               uint16        // prefix loaded from DB (must set at loading, and unset at eviction)
	hashes                     []common.Hash // TODO: store it as fixed size flat array?
	addrHashPrefix             []byte
}

func (ahi *AccountTrieItem) CopyValueFrom(item CacheItem) {
	other, ok := item.(*AccountTrieItem)
	if !ok {
		panic(fmt.Sprintf("expected AccountTrieItem, got %T", item))
	}
	ahi.hashes = make([]common.Hash, len(other.hashes))
	for i := 0; i < len(ahi.hashes); i++ {
		ahi.hashes[i] = other.hashes[i]
	}
	ahi.hasState = other.hasState
	ahi.hasTree = other.hasTree
	ahi.hasHash = other.hasHash
	ahi.loadedFromDB = other.loadedFromDB
}

func (ahi *AccountTrieItem) Less(than btree.Item) bool {
	switch i := than.(type) {
	case *AccountTrieItem:
		return bytes.Compare(ahi.addrHashPrefix, i.addrHashPrefix) < 0
	case *AccountTrieWriteItem:
		return bytes.Compare(ahi.addrHashPrefix, i.ai.addrHashPrefix) < 0
	default:
		panic(fmt.Sprintf("unexpected type: %T", than))
	}
}

func (ahi *AccountTrieItem) GetSequence() int         { return ahi.sequence }
func (ahi *AccountTrieItem) SetSequence(sequence int) { ahi.sequence = sequence }
func (ahi *AccountTrieItem) GetSize() int             { return accountHashItemSize + len(ahi.addrHashPrefix) }
func (ahi *AccountTrieItem) GetQueuePos() int         { return ahi.queuePos }
func (ahi *AccountTrieItem) SetQueuePos(pos int)      { ahi.queuePos = pos }
func (ahi *AccountTrieItem) HasFlag(flag uint16) bool { return ahi.flags&flag != 0 }
func (ahi *AccountTrieItem) SetFlags(flags uint16)    { ahi.flags |= flags }
func (ahi *AccountTrieItem) ClearFlags(flags uint16)  { ahi.flags &^= flags }
func (ahi *AccountTrieItem) String() string {
	return fmt.Sprintf("%T(addrHashPrefix=%x)", ahi, ahi.addrHashPrefix)
}

func (awi *AccountTrieWriteItem) GetCacheItem() CacheItem     { return awi.ai }
func (awi *AccountTrieWriteItem) SetCacheItem(item CacheItem) { awi.ai = item.(*AccountTrieItem) }
func (awi *AccountTrieWriteItem) GetSize() int                { return accountHashWriteItemSize }
func (awi *AccountTrieWriteItem) Less(than btree.Item) bool   { return awi.ai.Less(than) }

type StorageTrieWriteItem struct {
	i *StorageTrieItem
}

type StorageTrieItem struct {
	sequence, queuePos         int
	addrHash                   common.Hash
	incarnation                uint64
	flags                      uint16
	hasState, hasTree, hasHash uint16
	loadedFromDB               uint16 // prefix loaded from DB (must set at loading, and unset at eviction)
	locHashPrefix              []byte
	hashes                     []common.Hash
}

func (shi *StorageTrieItem) CopyValueFrom(item CacheItem) {
	other, ok := item.(*StorageTrieItem)
	if !ok {
		panic(fmt.Sprintf("expected StorageTrieItem, got %T", item))
	}
	shi.hashes = make([]common.Hash, len(other.hashes))
	for i := 0; i < len(shi.hashes); i++ {
		shi.hashes[i] = other.hashes[i]
	}
	shi.hasState = other.hasState
	shi.hasTree = other.hasTree
	shi.hasHash = other.hasHash
	shi.loadedFromDB = other.loadedFromDB
}

func (shi *StorageTrieItem) Less(than btree.Item) bool {
	i := than.(*StorageTrieItem)
	c := bytes.Compare(shi.addrHash.Bytes(), i.addrHash.Bytes())
	if c != 0 {
		return c < 0
	}
	if shi.incarnation != i.incarnation {
		return shi.incarnation < i.incarnation
	}
	return bytes.Compare(shi.locHashPrefix, i.locHashPrefix) < 0
}

func (shi *StorageTrieItem) GetSequence() int         { return shi.sequence }
func (shi *StorageTrieItem) SetSequence(sequence int) { shi.sequence = sequence }
func (shi *StorageTrieItem) GetSize() int             { return storageHashItemSize + len(shi.locHashPrefix) }
func (shi *StorageTrieItem) GetQueuePos() int         { return shi.queuePos }
func (shi *StorageTrieItem) SetQueuePos(pos int)      { shi.queuePos = pos }
func (shi *StorageTrieItem) HasFlag(flag uint16) bool { return shi.flags&flag != 0 }
func (shi *StorageTrieItem) SetFlags(flags uint16)    { shi.flags |= flags }
func (shi *StorageTrieItem) ClearFlags(flags uint16)  { shi.flags &^= flags }
func (shi *StorageTrieItem) String() string {
	return fmt.Sprintf("%T(addrHash=%x,incarnation=%d,locHashPrefix=%x)", shi, shi.addrHash, shi.incarnation, shi.locHashPrefix)
}

func (wi *StorageTrieWriteItem) GetCacheItem() CacheItem     { return wi.i }
func (wi *StorageTrieWriteItem) SetCacheItem(item CacheItem) { wi.i = item.(*StorageTrieItem) }
func (wi *StorageTrieWriteItem) GetSize() int                { return storageHashWriteItemSize }
func (wi *StorageTrieWriteItem) Less(than btree.Item) bool {
	return wi.i.Less(than.(*StorageTrieWriteItem).i)
}

// UnprocessedHeap is a priority queue of items that were modified after the last recalculation of the merkle tree
type UnprocessedHeap struct {
	items []CacheItem
}

func (uh UnprocessedHeap) Len() int           { return len(uh.items) }
func (uh UnprocessedHeap) Less(i, j int) bool { return uh.items[i].Less(uh.items[j]) }
func (uh UnprocessedHeap) Swap(i, j int)      { uh.items[i], uh.items[j] = uh.items[j], uh.items[i] }
func (uh *UnprocessedHeap) Push(x interface{}) {
	// Push and Pop use pointer receivers because they modify the slice's length,
	// not just its contents.
	uh.items = append(uh.items, x.(CacheItem))
}

func (uh *UnprocessedHeap) Pop() interface{} {
	cacheItem := uh.items[len(uh.items)-1]
	uh.items = uh.items[:len(uh.items)-1]
	return cacheItem
}

func (ai *AccountItem) HasPrefix(prefix CacheItem) bool {
	switch i := prefix.(type) {
	case *AccountItem:
		return ai.addrHash == i.addrHash && ai.account.Incarnation == i.account.Incarnation
	default:
		panic(fmt.Sprintf("unrecognised type of cache item: %T", prefix))
	}
}

func (si *StorageItem) HasPrefix(prefix CacheItem) bool {
	switch i := prefix.(type) {
	case *StorageItem:
		return si.addrHash == i.addrHash && si.incarnation == i.incarnation && si.locHash == i.locHash
	default:
		panic(fmt.Sprintf("unrecognised type of cache item: %T", prefix))
	}
}

func (ci *CodeItem) HasPrefix(prefix CacheItem) bool {
	switch i := prefix.(type) {
	case *CodeItem:
		return ci.addrHash == i.addrHash && ci.incarnation == i.incarnation
	default:
		panic(fmt.Sprintf("unrecognised type of cache item: %T", prefix))
	}
}

func (ahi *AccountTrieItem) HasPrefix(prefix CacheItem) bool {
	switch i := prefix.(type) {
	case *AccountTrieItem:
		return bytes.HasPrefix(ahi.addrHashPrefix, i.addrHashPrefix)
	default:
		panic(fmt.Sprintf("unrecognised type of cache item: %T", prefix))
	}
}

func (shi *StorageTrieItem) HasPrefix(prefix CacheItem) bool {
	switch i := prefix.(type) {
	case *StorageTrieItem:
		if shi.addrHash != i.addrHash || shi.incarnation != i.incarnation {
			return false
		}
		return bytes.HasPrefix(shi.locHashPrefix, i.locHashPrefix)
	default:
		panic(fmt.Sprintf("unrecognised type of cache item: %T", prefix))
	}
}

func (sc *StateCache) SetAccountTrieRead(prefix []byte, hasState, hasTree, hasHash uint16, hashes []common.Hash) {
	if bits.OnesCount16(hasHash) != len(hashes) {
		panic(fmt.Errorf("invariant bits.OnesCount16(hasHash) == len(hashes) failed: %d, %d, at %x", bits.OnesCount16(hasHash), len(hashes), prefix))
	}
	assertSubset(hasTree, hasState)
	assertSubset(hasHash, hasState)
	cpy := make([]common.Hash, len(hashes))
	for i := 0; i < len(hashes); i++ {
		cpy[i] = hashes[i]
	}
	ai := AccountTrieItem{
		addrHashPrefix: common.CopyBytes(prefix),
		hasState:       hasState,
		hasTree:        hasTree,
		hasHash:        hasHash,
		hashes:         cpy,
	}
	sc.setRead(&ai, false /* absent */)
}

func (sc *StateCache) SetAccountTrieWrite(prefix []byte, hasState, hasTree, hasHash uint16, hashes []common.Hash) {
	if bits.OnesCount16(hasHash) != len(hashes) {
		panic(fmt.Errorf("invariant bits.OnesCount16(hasHash) == len(hashes) failed: %d, %d", bits.OnesCount16(hasHash), len(hashes)))
	}
	assertSubset(hasTree, hasState)
	assertSubset(hasHash, hasState)
	ai := AccountTrieItem{
		addrHashPrefix: common.CopyBytes(prefix),
		hasState:       hasState,
		hasTree:        hasTree,
		hasHash:        hasHash,
		hashes:         make([]common.Hash, len(hashes)),
	}
	for i := 0; i < len(hashes); i++ {
		ai.hashes[i] = hashes[i]
	}
	var awi AccountTrieWriteItem
	awi.ai = &ai
	sc.setWrite(&ai, &awi, false /* delete */)
}

func (sc *StateCache) SetAccountTrieDelete(prefix []byte) {
	ai := AccountTrieItem{addrHashPrefix: common.CopyBytes(prefix)}
	var wi AccountTrieWriteItem
	wi.ai = &ai
	sc.setWrite(&ai, &wi, true /* delete */)
}

func (sc *StateCache) SetStorageTrieRead(addrHash common.Hash, incarnation uint64, locHashPrefix []byte, hasState, hasTree, hasHash uint16, hashes []common.Hash) {
	if bits.OnesCount16(hasHash) != len(hashes) {
		isValid := len(locHashPrefix) == 0 && bits.OnesCount16(hasHash)+1 != len(hashes)
		if !isValid {
			panic(fmt.Errorf("invariant bits.OnesCount16(hasHash) == len(hashes) failed: %d, %d", bits.OnesCount16(hasHash), len(hashes)))
		}
	}
	assertSubset(hasTree, hasState)
	assertSubset(hasHash, hasState)
	cpy := make([]common.Hash, len(hashes))
	for i := 0; i < len(hashes); i++ {
		cpy[i] = hashes[i]
	}
	ai := StorageTrieItem{
		addrHash:      addrHash,
		incarnation:   incarnation,
		locHashPrefix: common.CopyBytes(locHashPrefix),
		hasState:      hasState,
		hasTree:       hasTree,
		hasHash:       hasHash,
		hashes:        cpy,
	}
	sc.setRead(&ai, false /* absent */)
}

func (sc *StateCache) SetStorageTrieWrite(addrHash common.Hash, incarnation uint64, locHashPrefix []byte, hasState, hasTree, hasHash uint16, hashes []common.Hash) {
	if bits.OnesCount16(hasHash) != len(hashes) {
		isValid := len(locHashPrefix) == 0 && bits.OnesCount16(hasHash)+1 != len(hashes)
		if !isValid {
			panic(fmt.Errorf("invariant bits.OnesCount16(hasHash) == len(hashes) failed: %d, %d", bits.OnesCount16(hasHash), len(hashes)))
		}
	}
	assertSubset(hasTree, hasState)
	assertSubset(hasHash, hasState)
	cpy := make([]common.Hash, len(hashes))
	for i := 0; i < len(hashes); i++ {
		cpy[i] = hashes[i]
	}
	ai := StorageTrieItem{
		addrHash:      addrHash,
		incarnation:   incarnation,
		locHashPrefix: common.CopyBytes(locHashPrefix),
		hasState:      hasState,
		hasTree:       hasTree,
		hasHash:       hasHash,
		hashes:        cpy,
	}
	var wi StorageTrieWriteItem
	wi.i = &ai
	sc.setWrite(&ai, &wi, false /* delete */)
}

func (sc *StateCache) SetStorageTrieDelete(addrHash common.Hash, incarnation uint64, locHashPrefix []byte, hasState, hasTree, hasHash uint16, hashes []common.Hash) {
	if bits.OnesCount16(hasHash) != len(hashes) {
		isValid := len(locHashPrefix) == 0 && bits.OnesCount16(hasHash)+1 != len(hashes)
		if !isValid {
			panic(fmt.Errorf("invariant bits.OnesCount16(hasHash) == len(hashes) failed: %d, %d", bits.OnesCount16(hasHash), len(hashes)))
		}
	}
	assertSubset(hasTree, hasState)
	assertSubset(hasHash, hasState)
	cpy := make([]common.Hash, len(hashes))
	for i := 0; i < len(hashes); i++ {
		cpy[i] = hashes[i]
	}
	ai := StorageTrieItem{
		addrHash:      addrHash,
		incarnation:   incarnation,
		locHashPrefix: common.CopyBytes(locHashPrefix),
		hasState:      hasState,
		hasTree:       hasTree,
		hasHash:       hasHash,
		hashes:        cpy,
	}
	var wi StorageTrieWriteItem
	wi.i = &ai
	sc.setWrite(&ai, &wi, true /* delete */)
}

func (sc *StateCache) MarkAccountTrieAsLoaded(prefix []byte) {
	if item, ok := sc.get(&AccountTrieItem{addrHashPrefix: prefix[:len(prefix)-1]}); ok {
		item.(*AccountTrieItem).loadedFromDB |= 1 << prefix[len(prefix)-1]
	}
}

func (sc *StateCache) MarkStorageTrieAsLoaded(addrHash common.Hash, incarnation uint64, prefix []byte) {
	if item, ok := sc.get(&StorageTrieItem{addrHash: addrHash, incarnation: incarnation, locHashPrefix: prefix[:len(prefix)-1]}); ok {
		item.(*StorageTrieItem).loadedFromDB |= 1 << prefix[len(prefix)-1]
	}
}

func (sc *StateCache) FindDeepestAccountTrie(prefix []byte) (ihK []byte, childHasState, childLoaded, trieMiss bool) {
	for i := 1; i < len(prefix); i++ {
		k, hasState, hasTree, _, _, loaded := sc.AccountTrieSeek(prefix[:i])
		if k == nil || !bytes.HasPrefix(k, prefix[:i]) {
			if i == 1 {
				break
			}
			return prefix[:i], false, false, true
		}
		if !has(hasTree, prefix[i]) {
			return prefix[:len(k)], has(hasState, prefix[i]), has(loaded, prefix[i]), false
		}
	}
	return nil, false, false, false
}

func (sc *StateCache) FindDeepestStorageTrie(addrHash common.Hash, incarnation uint64, prefix []byte) (ihK []byte, childHasState, childLoaded, trieMiss bool) {
	for i := 1; i < len(prefix); i++ {
		k, hasState, hasTree, _, _, loaded := sc.StorageTrieSeek(addrHash, incarnation, prefix[:i])
		if k == nil || !bytes.HasPrefix(k, prefix[:i]) {
			if i == 1 {
				break
			}
			return prefix[:i], false, false, true
		}
		if !has(hasTree, prefix[i]) {
			return prefix[:len(k)], has(hasState, prefix[i]), has(loaded, prefix[i]), false
		}
	}
	return nil, false, false, false
}

func (sc *StateCache) GetAccountTrie(prefix []byte) ([]byte, uint16, uint16, uint16, []common.Hash, uint16, bool) {
	if item, ok := sc.get(&AccountTrieItem{addrHashPrefix: prefix}); ok && item != nil {
		i := item.(*AccountTrieItem)
		return i.addrHashPrefix, i.hasState, i.hasTree, i.hasHash, i.hashes, i.loadedFromDB, true
	}
	return nil, 0, 0, 0, nil, 0, false
}

func (sc *StateCache) GetStorageTrie(addrHash common.Hash, incarnation uint64, prefix []byte) ([]byte, uint16, uint16, uint16, []common.Hash, bool) {
	if item, ok := sc.get(&StorageTrieItem{addrHash: addrHash, incarnation: incarnation, locHashPrefix: prefix}); ok && item != nil {
		i := item.(*StorageTrieItem)
		return i.locHashPrefix, i.hasState, i.hasTree, i.hasHash, i.hashes, true
	}
	return nil, 0, 0, 0, nil, false
}

func (sc *StateCache) DebugPrintAccountsTrie() error {
	var cur *AccountTrieItem
	sc.readWrites[id(cur)].Ascend(func(i btree.Item) bool {
		switch it := i.(type) {
		case *AccountTrieItem:
			if it.HasFlag(AbsentFlag) || it.HasFlag(DeletedFlag) {
				fmt.Printf("deleted: %x\n", it.addrHashPrefix)
			} else if it.HasFlag(ModifiedFlag) {
				fmt.Printf("modified: %x\n", it.addrHashPrefix)
			} else {
				fmt.Printf("normal: %x\n", it.addrHashPrefix)
			}
		case *AccountTrieWriteItem:
			if it.ai.HasFlag(AbsentFlag) || it.ai.HasFlag(DeletedFlag) {
				fmt.Printf("deleted: %x\n", it.ai.addrHashPrefix)
			} else if it.ai.HasFlag(ModifiedFlag) {
				fmt.Printf("modified: %x\n", it.ai.addrHashPrefix)
			} else {
				fmt.Printf("normal: %x\n", it.ai.addrHashPrefix)
			}
		}
		return true
	})

	return nil
}

type Walker func(k []byte, h common.Hash, hasTree, hasHash bool) (toChild bool, err error)
type OnMiss func(k []byte)

func (sc *StateCache) AccountTree(logPrefix string, prefix []byte, walker Walker, onMiss OnMiss) (err error) {
	var cur []byte
	buf := make([]byte, 0, 64)
	next := make([]byte, 0, 64)
	var k [64][]byte
	var hasTree, hasState, hasHash [64]uint16
	var hashID [64]int16
	var id [64]int8
	var hashes [64][]common.Hash
	var lvl int
	var _hasChild = func() bool { return (1<<id[lvl])&hasState[lvl] != 0 }
	var _hasTree = func() bool { return (1<<id[lvl])&hasTree[lvl] != 0 }
	var _hasHash = func() bool { return (1<<id[lvl])&hasHash[lvl] != 0 }
	var _nextSiblingInMem = func() bool {
		for id[lvl]++; id[lvl] < int8(bits.Len16(hasState[lvl])); id[lvl]++ { // go to sibling
			if !_hasChild() {
				continue
			}

			if _hasHash() {
				hashID[lvl]++
			}
			return true
		}
		return false
	}
	var _unmarshal = func(ihK []byte, hasStateItem, hasTreeItem, hasHashItem uint16, hashItem []common.Hash) {
		from, to := lvl+1, len(k)
		if lvl >= len(k) {
			from, to = len(k)+1, lvl+2
		}
		for i := from; i < to; i++ { // if first meet key is not 0 length, then nullify all shorter metadata
			k[i], hasState[i], hasTree[i], hasHash[i], hashID[i], id[i], hashes[i] = nil, 0, 0, 0, 0, 0, nil
		}
		lvl = len(ihK)
		k[lvl], hasState[lvl], hasTree[lvl], hasHash[lvl], hashes[lvl] = ihK, hasStateItem, hasTreeItem, hasHashItem, hashItem
		hashID[lvl], id[lvl] = -1, int8(bits.TrailingZeros16(hasState[lvl]))-1
		_nextSiblingInMem()
	}
	var _seek = func(seek []byte, withinPrefix []byte) bool {
		ihK, hasStateItem, hasTreeItem, hasHashItem, hashItem, _ := sc.AccountTrieSeek(seek)
		if len(withinPrefix) > 0 { // seek within given prefix doesn't stop overall process, even if ihK==nil
			if ihK == nil {
				return false
			}
			if !bytes.HasPrefix(ihK, withinPrefix) {
				return false
			}
		} else { // seek in global prefix - does finish overall process
			if ihK == nil {
				k[lvl] = nil
				return false
			}
			if !bytes.HasPrefix(ihK, prefix) {
				k[lvl] = nil
				return false
			}
		}
		_unmarshal(ihK, hasStateItem, hasTreeItem, hasHashItem, hashItem)
		return true
	}
	var _nextSiblingOfParentInMem = func() bool {
		for lvl > 1 { // go to parent sibling in mem
			if k[lvl-1] == nil {
				nonNilLvl := lvl - 1
				for k[nonNilLvl] == nil && nonNilLvl > 1 {
					nonNilLvl--
				}
				if k[nonNilLvl] == nil { // if no parent found
					return false
				}
				next = append(append(next[:0], k[lvl]...), uint8(id[lvl]))
				buf = append(append(buf[:0], k[nonNilLvl]...), uint8(id[nonNilLvl]))
				if _seek(next, buf) {
					return true
				}
				lvl = nonNilLvl + 1
				continue
			}
			lvl--
			if _nextSiblingInMem() {
				return true
			}
		}
		return false
	}
	var _nextSiblingInDB = func() bool {
		if ok := dbutils.NextNibblesSubtree(k[lvl], &next); !ok {
			k[lvl] = nil
			return false
		}
		_seek(next, []byte{})
		return k[lvl] != nil
	}

	_seek(prefix, []byte{})

	var toChild bool
	var hash common.Hash
	for k[lvl] != nil { // go to sibling in cache
		cur = append(append(cur[:0], k[lvl]...), uint8(id[lvl]))
		if _hasHash() {
			hash = hashes[lvl][hashID[lvl]]
		}
		toChild, err = walker(cur, hash, _hasTree(), _hasHash())
		if err != nil {
			return err
		}

		// preOrderTraversalStep
		if toChild && _hasTree() {
			if _seek(cur, cur) {
				continue
			}

			onMiss(cur)
		}
		_ = _nextSiblingInMem() || _nextSiblingOfParentInMem() || _nextSiblingInDB()
	}

	if _, err = walker(nil, common.Hash{}, false, false); err != nil {
		return err
	}
	return nil
}

func (sc *StateCache) StorageTree(logPrefix string, accHash common.Hash, incarnation uint64, walker Walker, onMiss OnMiss) (err error) {
	var cur []byte
	buf := make([]byte, 0, 64)
	next := make([]byte, 0, 64)
	var k [64][]byte
	var hasTree, hasState, hasHash [64]uint16
	var hashID [64]int16
	var id [64]int8
	var hashes [64][]common.Hash
	var lvl int
	var _hasChild = func() bool { return (1<<id[lvl])&hasState[lvl] != 0 }
	var _hasTree = func() bool { return (1<<id[lvl])&hasTree[lvl] != 0 }
	var _hasHash = func() bool { return (1<<id[lvl])&hasHash[lvl] != 0 }
	var _nextSiblingInMem = func() bool {
		for id[lvl]++; id[lvl] < int8(bits.Len16(hasState[lvl])); id[lvl]++ { // go to sibling
			if !_hasChild() {
				continue
			}

			if _hasHash() {
				hashID[lvl]++
			}
			return true
		}
		return false
	}
	var _unmarshal = func(ihK []byte, hasStateItem, hasTreeItem, hasHashItem uint16, hashItem []common.Hash) {
		from, to := lvl+1, len(k)
		if lvl >= len(k) {
			from, to = len(k)+1, lvl+2
		}
		for i := from; i < to; i++ { // if first meet key is not 0 length, then nullify all shorter metadata
			k[i], hasState[i], hasTree[i], hasHash[i], hashID[i], id[i], hashes[i] = nil, 0, 0, 0, 0, 0, nil
		}
		lvl = len(ihK)
		k[lvl], hasState[lvl], hasTree[lvl], hasHash[lvl], hashes[lvl] = ihK, hasStateItem, hasTreeItem, hasHashItem, hashItem
		hashID[lvl], id[lvl] = -1, int8(bits.TrailingZeros16(hasState[lvl]))-1
		_nextSiblingInMem()
	}
	var _seek = func(seek []byte, withinPrefix []byte) bool {
		ihK, hasStateItem, hasTreeItem, hasHashItem, hashItem, _ := sc.StorageTrieSeek(accHash, incarnation, seek)
		if len(withinPrefix) > 0 { // seek within given prefix doesn't stop overall process, even if ihK==nil
			if ihK == nil {
				return false
			}
			if !bytes.HasPrefix(ihK, withinPrefix) {
				return false
			}
		} else { // seek in global prefix - does finish overall process
			if ihK == nil {
				k[lvl] = nil
				return false
			}
		}
		_unmarshal(ihK, hasStateItem, hasTreeItem, hasHashItem, hashItem)
		return true
	}
	var _nextSiblingOfParentInMem = func() bool {
		for lvl > 0 { // go to parent sibling in mem
			if k[lvl-1] == nil {
				nonNilLvl := lvl - 1
				for k[nonNilLvl] == nil && nonNilLvl > 1 {
					nonNilLvl--
				}
				if k[nonNilLvl] == nil { // if no parent found
					return false
				}
				next = append(append(next[:0], k[lvl]...), uint8(id[lvl]))
				buf = append(append(buf[:0], k[nonNilLvl]...), uint8(id[nonNilLvl]))
				if _seek(next, buf) {
					return true
				}
				lvl = nonNilLvl + 1
				continue
			}
			lvl--
			if _nextSiblingInMem() {
				return true
			}
		}
		return false
	}
	var _nextSiblingInDB = func() bool {
		if ok := dbutils.NextNibblesSubtree(k[lvl], &next); !ok {
			k[lvl] = nil
			return false
		}
		_seek(next, []byte{})
		return k[lvl] != nil
	}

	_seek([]byte{}, []byte{})

	var toChild bool
	var hash common.Hash
	for k[lvl] != nil { // go to sibling in cache
		cur = append(append(cur[:0], k[lvl]...), uint8(id[lvl]))
		if _hasHash() {
			hash = hashes[lvl][hashID[lvl]]
		}
		toChild, err = walker(cur, hash, _hasTree(), _hasHash())
		if err != nil {
			return err
		}

		// preOrderTraversalStep
		if toChild && _hasTree() {
			if _seek(cur, cur) {
				continue
			}
			onMiss(cur)
		}
		_ = _nextSiblingInMem() || _nextSiblingOfParentInMem() || _nextSiblingInDB()
	}

	if _, err = walker(nil, common.Hash{}, false, false); err != nil {
		return err
	}
	return nil
}

func (sc *StateCache) AccountTrieSeek(prefix []byte) ([]byte, uint16, uint16, uint16, []common.Hash, uint16) {
	var found *AccountTrieItem
	seek := &AccountTrieItem{addrHashPrefix: prefix}
	sc.readWrites[id(seek)].AscendGreaterOrEqual(seek, func(i btree.Item) bool {
		it := i.(*AccountTrieItem)
		if it.HasFlag(AbsentFlag) || it.HasFlag(DeletedFlag) {
			return true
		}
		found = it // found
		return false
	})
	if found == nil {
		return nil, 0, 0, 0, nil, 0
	}
	return found.addrHashPrefix, found.hasState, found.hasTree, found.hasHash, found.hashes, found.loadedFromDB
}

func (sc *StateCache) HasAccountTrieWithPrefix(prefix []byte) bool {
	found, _, _, _, _, _ := sc.AccountTrieSeek(prefix)
	return bytes.HasPrefix(found, prefix)
}

func (sc *StateCache) StorageTrieSeek(addrHash common.Hash, incarnation uint64, prefix []byte) ([]byte, uint16, uint16, uint16, []common.Hash, uint16) {
	var found *StorageTrieItem
	seek := &StorageTrieItem{addrHash: addrHash, incarnation: incarnation, locHashPrefix: prefix}
	sc.readWrites[id(seek)].AscendGreaterOrEqual(seek, func(i btree.Item) bool {
		it := i.(*StorageTrieItem)
		if it.HasFlag(AbsentFlag) || it.HasFlag(DeletedFlag) {
			return true
		}
		if it.addrHash != addrHash || it.incarnation != incarnation {
			return false
		}
		found = it
		return false
	})
	if found == nil {
		return nil, 0, 0, 0, nil, 0
	}
	return found.locHashPrefix, found.hasState, found.hasTree, found.hasHash, found.hashes, found.loadedFromDB
}

func WalkAccountHashesWrites(writes [5]*btree.BTree, update func(prefix []byte, hasState, hasTree, hasHash uint16, h []common.Hash), del func(prefix []byte, hasState, hasTree, hasHash uint16, h []common.Hash)) {
	id := id(&AccountTrieWriteItem{})
	writes[id].Ascend(func(i btree.Item) bool {
		it := i.(*AccountTrieWriteItem)
		if it.ai.HasFlag(AbsentFlag) || it.ai.HasFlag(DeletedFlag) {
			del(it.ai.addrHashPrefix, it.ai.hasState, it.ai.hasTree, it.ai.hasHash, it.ai.hashes)
			return true
		}
		update(it.ai.addrHashPrefix, it.ai.hasState, it.ai.hasTree, it.ai.hasHash, it.ai.hashes)
		return true
	})
}

func (sc *StateCache) WalkStorageHashes(walker func(addrHash common.Hash, incarnation uint64, prefix []byte, hasStat, hasTree, hasHash uint16, h []common.Hash) error) error {
	sc.readWrites[id(&StorageTrieItem{})].Ascend(func(i btree.Item) bool {
		it, ok := i.(*StorageTrieItem)
		if !ok {
			return true
		}
		if it.HasFlag(AbsentFlag) || it.HasFlag(DeletedFlag) {
			return true
		}
		if err := walker(it.addrHash, it.incarnation, it.locHashPrefix, it.hasState, it.hasTree, it.hasHash, it.hashes); err != nil {
			panic(err)
		}
		return true
	})
	return nil
}

func WalkStorageHashesWrites(writes [5]*btree.BTree, update func(addrHash common.Hash, incarnation uint64, locHashPrefix []byte, hasState, hasTree, hasHash uint16, h []common.Hash), del func(addrHash common.Hash, incarnation uint64, locHashPrefix []byte, hasStat, hasTree, hasHash uint16, h []common.Hash)) {
	writes[id(&StorageWriteItem{})].Ascend(func(i btree.Item) bool {
		it := i.(*StorageTrieWriteItem)
		if it.i.HasFlag(AbsentFlag) || it.i.HasFlag(DeletedFlag) {
			del(it.i.addrHash, it.i.incarnation, it.i.locHashPrefix, it.i.hasState, it.i.hasTree, it.i.hasHash, it.i.hashes)
			return true
		}
		update(it.i.addrHash, it.i.incarnation, it.i.locHashPrefix, it.i.hasState, it.i.hasTree, it.i.hasHash, it.i.hashes)
		return true
	})
}

func (sc *StateCache) WalkStorage(addrHash common.Hash, incarnation uint64, prefix []byte, walker func(locHash common.Hash, val []byte) error) error {
	fixedbytes, mask := ethdb.Bytesmask(len(prefix) * 8)
	seek := &StorageSeek{seek: prefix, fixedBytes: fixedbytes - 1, mask: mask}
	sc.readWrites[id(seek)].AscendGreaterOrEqual(seek, func(i btree.Item) bool {
		switch it := i.(type) {
		case *StorageItem:
			if it.HasFlag(AbsentFlag) || it.HasFlag(DeletedFlag) {
				return true
			}
			if it.addrHash != addrHash || it.incarnation != incarnation {
				return false
			}
			if err := walker(it.locHash, it.value.Bytes()); err != nil {
				panic(err)
			}
		case *StorageWriteItem:
			if it.si.HasFlag(AbsentFlag) || it.si.HasFlag(DeletedFlag) {
				return true
			}
			if it.si.addrHash != addrHash || it.si.incarnation != incarnation {
				return false
			}
			if err := walker(it.si.locHash, it.si.value.Bytes()); err != nil {
				panic(err)
			}
		}
		return true
	})
	return nil
}

func (sc *StateCache) WalkAccounts(prefix []byte, walker func(addrHash common.Hash, acc *accounts.Account) (bool, error)) error {
	fixedbytes, mask := ethdb.Bytesmask(len(prefix) * 8)
	seek := &AccountSeek{seek: prefix, fixedBytes: fixedbytes - 1, mask: mask}
	sc.readWrites[id(seek)].AscendGreaterOrEqual(seek, func(i btree.Item) bool {
		switch it := i.(type) {
		case *AccountItem:
			if it.HasFlag(AbsentFlag) || it.HasFlag(DeletedFlag) {
				return true
			}
			if goOn, err := walker(it.addrHash, &it.account); err != nil {
				panic(err)
			} else if !goOn {
				return false
			}
		case *AccountWriteItem:
			if it.ai.HasFlag(AbsentFlag) || it.ai.HasFlag(DeletedFlag) {
				return true
			}
			if goOn, err := walker(it.ai.addrHash, &it.ai.account); err != nil {
				panic(err)
			} else if !goOn {
				return false
			}
		}
		return true
	})
	return nil
}

func assertSubset(a, b uint16) {
	if (a & b) != a { // a & b == a - checks whether a is subset of b
		panic(fmt.Errorf("invariant 'is subset' failed: %b, %b", a, b))
	}
}

// has - returns true if bit `pos` is set in `bitset`
func has(bitset uint16, pos uint8) bool { return 1<<pos&bitset != 0 }
