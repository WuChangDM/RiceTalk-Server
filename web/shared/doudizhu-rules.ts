export interface DoudizhuCard {
  rank: number
  suit: string
  value: string
}

export interface DoudizhuCardLike {
  rank: number
}

export interface DoudizhuPattern {
  kind: string
  mainRank: number
  length: number
}

const RANK2_IDX = 12
const BLACK_JOKER_IDX = 13
const RED_JOKER_IDX = 14
const MAX_CHAIN_IDX = 11

export function doudizhuRuleRank(card: DoudizhuCardLike): number {
  return card.rank - 3
}

function handToVector(hand: DoudizhuCardLike[]): number[] {
  const vec = new Array(15).fill(0)
  for (const c of hand) {
    const r = doudizhuRuleRank(c)
    if (r >= 0 && r < 15) vec[r]++
  }
  return vec
}

function isConsecutive(ranks: number[], maxRank: number): boolean {
  if (ranks.length === 0) return false
  ranks = [...ranks].sort((a, b) => a - b)
  if (ranks[ranks.length - 1] > maxRank) return false
  for (let i = 1; i < ranks.length; i++) {
    if (ranks[i] !== ranks[i - 1] + 1) return false
  }
  return true
}

function noOverlap(a: number[], b: number[]): boolean {
  const set = new Set(a)
  for (const x of b) if (set.has(x)) return false
  return true
}

export function parseDoudizhuPlay(cards: DoudizhuCardLike[] | null | undefined): DoudizhuPattern | null {
  if (!cards) return null
  const n = cards.length
  if (n === 0) return null

  const counts = new Map<number, number>()
  for (const c of cards) {
    const r = doudizhuRuleRank(c)
    if (r < 0 || r > RED_JOKER_IDX) return null
    counts.set(r, (counts.get(r) || 0) + 1)
  }
  for (const [r, c] of counts) {
    if (r <= RANK2_IDX && c > 4) return null
    if (r > RANK2_IDX && c > 1) return null
  }

  const ranks = Array.from(counts.keys()).sort((a, b) => a - b)
  const getCounts = (target: number): number[] =>
    ranks.filter((r) => counts.get(r) === target)

  switch (n) {
    case 1:
      return { kind: 'solo', mainRank: doudizhuRuleRank(cards[0]), length: 1 }
    case 2: {
      const r0 = doudizhuRuleRank(cards[0])
      const r1 = doudizhuRuleRank(cards[1])
      if ((r0 === RED_JOKER_IDX && r1 === BLACK_JOKER_IDX) || (r0 === BLACK_JOKER_IDX && r1 === RED_JOKER_IDX)) {
        return { kind: 'rocket', mainRank: RED_JOKER_IDX, length: 1 }
      }
      if (r0 === r1) return { kind: 'pair', mainRank: r0, length: 1 }
      return null
    }
    case 3: {
      const r0 = doudizhuRuleRank(cards[0])
      if (counts.get(r0) === 3) return { kind: 'trio', mainRank: r0, length: 1 }
      return null
    }
    case 4: {
      for (const [r, c] of counts) {
        if (c === 4) return { kind: 'bomb', mainRank: r, length: 1 }
      }
      const trios = getCounts(3)
      const solos = getCounts(1)
      if (trios.length === 1 && solos.length === 1) {
        return { kind: 'trioWithSolo', mainRank: trios[0], length: 1 }
      }
      return null
    }
  }

  const solos = getCounts(1)
  const pairs = getCounts(2)
  const trios = getCounts(3)
  const fours = getCounts(4)

  if (solos.length === n && n >= 5 && pairs.length === 0 && trios.length === 0 && fours.length === 0) {
    if (isConsecutive(solos, MAX_CHAIN_IDX)) {
      return { kind: 'chain', mainRank: Math.min(...solos), length: solos.length }
    }
  }

  if (pairs.length * 2 === n && pairs.length >= 3 && solos.length === 0 && trios.length === 0 && fours.length === 0) {
    if (isConsecutive(pairs, MAX_CHAIN_IDX)) {
      return { kind: 'pairsChain', mainRank: Math.min(...pairs), length: pairs.length }
    }
  }

  if (trios.length * 3 === n && trios.length >= 2 && solos.length === 0 && pairs.length === 0 && fours.length === 0) {
    if (isConsecutive(trios, MAX_CHAIN_IDX)) {
      return { kind: 'airplane', mainRank: Math.min(...trios), length: trios.length }
    }
  }

  if (trios.length === 1 && n === 5 && pairs.length === 1 && solos.length === 0 && fours.length === 0) {
    return { kind: 'trioWithPair', mainRank: trios[0], length: 1 }
  }

  if (trios.length >= 2 && trios.length * 5 === n && pairs.length === trios.length && solos.length === 0 && fours.length === 0) {
    if (isConsecutive(trios, MAX_CHAIN_IDX) && noOverlap(trios, pairs)) {
      return { kind: 'airplaneWithPairs', mainRank: Math.min(...trios), length: trios.length }
    }
  }

  if (trios.length >= 2 && trios.length * 4 === n && solos.length === trios.length && pairs.length === 0 && fours.length === 0) {
    if (isConsecutive(trios, MAX_CHAIN_IDX) && noOverlap(trios, solos)) {
      return { kind: 'airplaneWithSolos', mainRank: Math.min(...trios), length: trios.length }
    }
  }

  if (fours.length === 1 && n === 6 && solos.length === 2 && pairs.length === 0 && trios.length === 0) {
    return { kind: 'fourWithDualSolo', mainRank: fours[0], length: 1 }
  }

  if (fours.length === 1 && n === 8 && pairs.length === 2 && solos.length === 0 && trios.length === 0) {
    return { kind: 'fourWithDualPair', mainRank: fours[0], length: 1 }
  }

  return null
}

export function doudizhuPlayBeats(play: DoudizhuPattern, last: DoudizhuPattern | null): boolean {
  if (!last || last.kind === '') return true
  if (play.kind === 'rocket') return true
  if (last.kind === 'rocket') return false
  if (play.kind === 'bomb') return last.kind !== 'bomb' || play.mainRank > last.mainRank
  if (last.kind === 'bomb') return false
  if (play.kind !== last.kind || play.length !== last.length) return false
  return play.mainRank > last.mainRank
}

export function doudizhuPatternName(pattern: DoudizhuPattern | null | undefined): string {
  if (!pattern) return '无效牌型'
  const names: Record<string, string> = {
    solo: '单张',
    pair: '对子',
    trio: '三张',
    bomb: '炸弹',
    rocket: '火箭',
    trioWithSolo: '三带一',
    trioWithPair: '三带二',
    chain: '顺子',
    pairsChain: '连对',
    airplane: '飞机',
    airplaneWithSolos: '飞机带翅膀',
    airplaneWithPairs: '飞机带对',
    fourWithDualSolo: '四带二',
    fourWithDualPair: '四带两对',
  }
  return names[pattern.kind] || pattern.kind
}

export function hintDoudizhuPlay<T extends DoudizhuCardLike>(hand: T[] | null | undefined, lastPlay: T[] | null | undefined): T[] | null {
  if (!hand) return null
  const vec = handToVector(hand)
  const last = lastPlay && lastPlay.length > 0 ? parseDoudizhuPlay(lastPlay) : null

  const findCards = (rank: number, count: number): T[] => {
    const res: T[] = []
    for (const c of hand!) {
      if (doudizhuRuleRank(c) === rank && res.length < count) res.push(c)
    }
    return res
  }

  // Rocket always beats non-rocket plays.
  if (vec[RED_JOKER_IDX] > 0 && vec[BLACK_JOKER_IDX] > 0) {
    if (!last || last.kind !== 'rocket') {
      return [...findCards(BLACK_JOKER_IDX, 1), ...findCards(RED_JOKER_IDX, 1)]
    }
  }

  // No active play: lead with the smallest single card.
  if (!last) {
    for (let r = 0; r <= RED_JOKER_IDX; r++) {
      if (vec[r] > 0) return findCards(r, 1)
    }
    return null
  }

  // For complex patterns, try bomb first (already handled above for non-rocket last),
  // then give up and let the player choose manually.
  if (last.kind !== 'rocket') {
    const start = last.kind === 'bomb' ? last.mainRank + 1 : 0
    for (let r = start; r <= RANK2_IDX; r++) {
      if (vec[r] === 4) return findCards(r, 4)
    }
  }

  switch (last.kind) {
    case 'solo':
      for (let r = last.mainRank + 1; r <= RED_JOKER_IDX; r++) {
        if (vec[r] > 0) return findCards(r, 1)
      }
      break
    case 'pair':
      for (let r = last.mainRank + 1; r <= RANK2_IDX; r++) {
        if (vec[r] >= 2) return findCards(r, 2)
      }
      break
    case 'trio':
      for (let r = last.mainRank + 1; r <= RANK2_IDX; r++) {
        if (vec[r] >= 3) return findCards(r, 3)
      }
      break
    case 'trioWithSolo':
      for (let r = last.mainRank + 1; r <= RANK2_IDX; r++) {
        if (vec[r] >= 3) {
          const trio = findCards(r, 3)
          for (let k = 0; k <= RED_JOKER_IDX; k++) {
            if (k !== r && vec[k] > 0) return [...trio, ...findCards(k, 1)]
          }
        }
      }
      break
    case 'trioWithPair':
      for (let r = last.mainRank + 1; r <= RANK2_IDX; r++) {
        if (vec[r] >= 3) {
          const trio = findCards(r, 3)
          for (let k = 0; k <= RANK2_IDX; k++) {
            if (k !== r && vec[k] >= 2) return [...trio, ...findCards(k, 2)]
          }
        }
      }
      break
  }

  return null
}
