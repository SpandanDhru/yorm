import type Konva from "konva";
import { useEffect, useRef, useState } from "react";
import { Circle, Group, Image as KImage, Layer, Line, Rect, Shape, Stage, Text } from "react-konva";
import { pathCost } from "../move";
import { canControl, cellKey, fogView, hpFraction, hpLevel, rectCells, tokenPos, type TableState } from "../store";
import type { Cell, Rect as CellRect, MapInfo, Member, TerrainKind, Token, TokenID, UserID } from "../types";

const CELL = 64; // px per grid cell at zoom 1
const MIN_ZOOM = 0.1;
const MAX_ZOOM = 5;

export type PaintKind = TerrainKind | "clear" | "reveal" | "cover";
type Mode = "select" | "brush" | "rect";

export interface Paint {
  terrain: PaintKind;
  for?: UserID; // reveal and cover: a player, or undefined for the party
  cells?: Cell[];
  rect?: CellRect;
}

interface Props {
  table: TableState;
  me: UserID; // whose view this is: the user, or the player the DM is viewing as
  isDM: boolean; // may use the DM's tools
  dmView: boolean; // sees the DM's view (false while the DM views as a player)
  selected: TokenID | null;
  onSelect(id: TokenID | null): void;
  // onMove returns false if the move could not be sent.
  onMove(id: TokenID, to: Cell): boolean;
  onPaint(p: Paint): void;
}

export const TERRAIN: Record<PaintKind, { label: string; fill: string; hatch?: boolean }> = {
  wall: { label: "Wall", fill: "rgba(38,38,44,0.93)" },
  difficult: { label: "Difficult", fill: "rgba(139,90,43,0.45)", hatch: true },
  water: { label: "Water", fill: "rgba(47,127,209,0.5)" },
  hazard: { label: "Hazard", fill: "rgba(208,69,58,0.45)" },
  clear: { label: "Erase", fill: "rgba(255,255,255,0.35)" },
  reveal: { label: "Reveal", fill: "rgba(255,236,160,0.45)" },
  cover: { label: "Cover", fill: "rgba(12,13,17,0.75)" },
};

const HP_COLORS = { healthy: "#4caf7a", bloodied: "#e0a030", down: "#555555" };

const TERRAIN_KINDS: PaintKind[] = ["wall", "difficult", "water", "hazard", "clear"];
const FOG_KINDS: PaintKind[] = ["reveal", "cover"];

export function MapCanvas({ table, me, isDM, dmView, selected, onSelect, onMove, onPaint }: Props) {
  const wrap = useRef<HTMLDivElement>(null);
  const size = useSize(wrap);
  const map = table.game?.map ?? null;
  const image = useImage(map?.image_url || undefined);
  const [view, setView] = useState({ x: 0, y: 0, scale: 1 });
  const [mode, setMode] = useState<Mode>("select");
  const [brush, setBrush] = useState<PaintKind>("wall");
  const [fogFor, setFogFor] = useState<UserID | undefined>(undefined); // fog tools: the party, or one player
  const painting = isDM && mode !== "select";

  // Fit the whole map in view when it first appears or its grid changes.
  const hasSize = size.w > 0;
  useEffect(() => {
    if (!map || !hasSize) return;
    const w = map.cols * CELL;
    const h = map.rows * CELL;
    const scale = Math.min(size.w / w, size.h / h, 2) * 0.95;
    setView({ scale, x: (size.w - w * scale) / 2, y: (size.h - h * scale) / 2 });
    // Deliberately not refitting on every resize, which would undo the user's pan and zoom.
  }, [map?.id, map?.cols, map?.rows, hasSize]);

  function zoom(e: Konva.KonvaEventObject<WheelEvent>) {
    e.evt.preventDefault();
    const p = e.target.getStage()?.getPointerPosition();
    if (!p) return;
    const scale = clamp(view.scale * (e.evt.deltaY > 0 ? 1 / 1.1 : 1.1), MIN_ZOOM, MAX_ZOOM);
    // Keep the point under the cursor fixed.
    const mx = (p.x - view.x) / view.scale;
    const my = (p.y - view.y) / view.scale;
    setView({ scale, x: p.x - mx * scale, y: p.y - my * scale });
  }

  function dropToken(t: Token, from: Cell, node: Konva.Node) {
    if (!map) return;
    const to = {
      x: clamp(Math.round(node.x() / CELL), 0, map.cols - t.size),
      y: clamp(Math.round(node.y() / CELL), 0, map.rows - t.size),
    };
    const moved = (to.x !== from.x || to.y !== from.y) && onMove(t.id, to);
    // React only repositions the node if its props change, so snap it here.
    const at = moved ? to : from;
    node.position({ x: at.x * CELL, y: at.y * CELL });
  }

  // The cell a token is being dragged over, and what getting there costs.
  const [dragging, setDragging] = useState<{ token: TokenID; to: Cell; feet: number | null; over: boolean } | null>(null);

  function dragMove(t: Token, from: Cell, node: Konva.Node) {
    if (!map) return;
    const to = {
      x: clamp(Math.round(node.x() / CELL), 0, map.cols - t.size),
      y: clamp(Math.round(node.y() / CELL), 0, map.rows - t.size),
    };
    if (dragging?.token === t.id && dragging.to.x === to.x && dragging.to.y === to.y) return;
    const feet = pathCost(map, table.game!.settings.diagonal, t.size, from, to);
    const e = table.game!.encounter;
    const over = !!e?.active && e.active === t.actor && feet !== null && feet > e.economy.movement_left;
    setDragging({ token: t.id, to, feet, over });
  }

  // A stroke in progress: shown locally, sent as one command when released.
  const stroke = useRef<{ start: Cell; last: Cell; cells: Map<string, Cell> } | null>(null);
  const [preview, setPreview] = useState<Cell[]>([]);

  function cellAt(e: Konva.KonvaEventObject<PointerEvent>): Cell | null {
    const p = e.target.getStage()?.getRelativePointerPosition();
    if (!p || !map) return null;
    return { x: clamp(Math.floor(p.x / CELL), 0, map.cols - 1), y: clamp(Math.floor(p.y / CELL), 0, map.rows - 1) };
  }

  function strokeStart(e: Konva.KonvaEventObject<PointerEvent>) {
    const c = painting && cellAt(e);
    if (!c) return;
    stroke.current = { start: c, last: c, cells: new Map([[cellKey(c), c]]) };
    setPreview([c]);
  }

  function strokeMove(e: Konva.KonvaEventObject<PointerEvent>) {
    const s = stroke.current;
    const c = s && cellAt(e);
    if (!s || !c || (c.x === s.last.x && c.y === s.last.y)) return;
    if (mode === "brush") {
      for (const p of lineCells(s.last, c)) s.cells.set(cellKey(p), p); // no gaps on fast strokes
      setPreview([...s.cells.values()]);
    } else {
      setPreview(rectCells({ from: s.start, to: c }));
    }
    s.last = c;
  }

  function strokeEnd() {
    const s = stroke.current;
    if (!s) return;
    stroke.current = null;
    setPreview([]);
    const target = FOG_KINDS.includes(brush) ? fogFor : undefined;
    if (mode === "brush") onPaint({ terrain: brush, for: target, cells: [...s.cells.values()] });
    else onPaint({ terrain: brush, for: target, rect: { from: s.start, to: s.last } });
  }

  return (
    <div className="canvas" ref={wrap}>
      {!map && (
        <div className="canvas-empty">{isDM ? "Upload a map or create a blank one to start." : "Waiting for the DM to set up a map…"}</div>
      )}
      {map && isDM && (
        <Toolbar
          mode={mode}
          setMode={setMode}
          brush={brush}
          setBrush={setBrush}
          fog={map.fog.enabled}
          fogFor={fogFor}
          setFogFor={setFogFor}
          players={Object.values(table.game!.members).filter((m) => m.role === "player")}
        />
      )}
      {map && hasSize && (
        <Stage
          width={size.w}
          height={size.h}
          x={view.x}
          y={view.y}
          scaleX={view.scale}
          scaleY={view.scale}
          draggable={!painting}
          className={painting ? "painting" : undefined}
          onWheel={zoom}
          onDragEnd={(e) => {
            if (e.target === e.target.getStage()) setView((v) => ({ ...v, x: e.target.x(), y: e.target.y() }));
          }}
          onPointerDown={(e) => {
            if (painting) strokeStart(e);
            else if (e.target === e.target.getStage() || e.target.name() === "background") onSelect(null);
          }}
          onPointerMove={strokeMove}
          onPointerUp={strokeEnd}
          onPointerLeave={strokeEnd}
        >
          <Layer listening={!painting}>
            <Rect name="background" width={map.cols * CELL} height={map.rows * CELL} fill={map.background || "#2b2f36"} />
            {image && <KImage name="background" image={image} width={map.cols * CELL} height={map.rows * CELL} />}
          </Layer>
          <Layer listening={false}>
            <Shape sceneFunc={(ctx) => drawCells(ctx, Object.entries(map.terrain).map(([k, kind]) => [parseKey(k), kind]))} />
            <Grid cols={map.cols} rows={map.rows} />
          </Layer>
          {map.fog.enabled && (
            <Layer listening={false}>
              <Shape sceneFunc={(ctx) => drawFog(ctx, map, fogView(map, dmView ? fogFor : me), dmView)} />
            </Layer>
          )}
          <Layer listening={false}>
            {preview.length > 0 && <Shape sceneFunc={(ctx) => drawCells(ctx, preview.map((c) => [c, brush]))} />}
          </Layer>
          <Layer listening={!painting}>
            {Object.values(table.game!.tokens).map((t) => {
              const g = table.game!;
              const pos = tokenPos(table, t.id) ?? t.pos;
              const actor = t.actor ? g.actors[t.actor] : undefined;
              const label = actor?.name ?? t.label;
              const movable = canControl(g, me, t);
              const mine = movable && !isDM;
              const px = t.size * CELL;
              return (
                <Group
                  key={t.id}
                  x={pos.x * CELL}
                  y={pos.y * CELL}
                  opacity={t.hidden ? 0.45 : 1}
                  draggable={movable}
                  onPointerDown={() => onSelect(t.id)}
                  onDragMove={(e) => {
                    e.cancelBubble = true;
                    dragMove(t, pos, e.target);
                  }}
                  onDragEnd={(e) => {
                    e.cancelBubble = true; // don't pan the stage
                    setDragging(null);
                    dropToken(t, pos, e.target);
                  }}
                >
                  {g.encounter?.active && g.encounter.active === t.actor && (
                    <Circle x={px / 2} y={px / 2} radius={px / 2} stroke="#ffd166" strokeWidth={4} dash={[10, 6]} listening={false} />
                  )}
                  <Circle
                    x={px / 2}
                    y={px / 2}
                    radius={px / 2 - 4}
                    fill={t.color}
                    stroke={selected === t.id ? "#ffffff" : mine ? "#ffd166" : "#111111"}
                    strokeWidth={selected === t.id ? 4 : 2}
                    dash={t.hidden ? [6, 4] : undefined}
                    shadowColor="black"
                    shadowBlur={6}
                    shadowOpacity={0.5}
                  />
                  <Text
                    text={initials(label)}
                    width={px}
                    height={px}
                    align="center"
                    verticalAlign="middle"
                    fontSize={px * 0.32}
                    fontStyle="bold"
                    fill="#ffffff"
                    listening={false}
                  />
                  {actor && (
                    <Rect
                      x={6}
                      y={-2}
                      width={(px - 12) * hpFraction(actor)}
                      height={6}
                      cornerRadius={3}
                      fill={HP_COLORS[hpLevel(actor)]}
                      listening={false}
                    />
                  )}
                  <Text
                    text={label}
                    y={px - 2}
                    width={px}
                    align="center"
                    fontSize={16}
                    fontStyle="bold"
                    fill="#ffffff"
                    shadowColor="black"
                    shadowBlur={4}
                    shadowOpacity={1}
                    listening={false}
                  />
                </Group>
              );
            })}
            {dragging && (
              <Text
                x={dragging.to.x * CELL}
                y={dragging.to.y * CELL - 22}
                width={CELL * (table.game!.tokens[dragging.token]?.size ?? 1)}
                align="center"
                text={dragging.feet === null ? "blocked" : `${dragging.feet} ft`}
                fontSize={18}
                fontStyle="bold"
                fill={dragging.feet === null || dragging.over ? "#ff6b5e" : "#ffffff"}
                shadowColor="black"
                shadowBlur={4}
                shadowOpacity={1}
                listening={false}
              />
            )}
          </Layer>
        </Stage>
      )}
    </div>
  );
}

function Toolbar(props: {
  mode: Mode;
  setMode(m: Mode): void;
  brush: PaintKind;
  setBrush(k: PaintKind): void;
  fog: boolean;
  fogFor: UserID | undefined;
  setFogFor(u: UserID | undefined): void;
  players: Member[];
}) {
  const kinds = props.fog ? [...TERRAIN_KINDS, ...FOG_KINDS] : TERRAIN_KINDS;
  const modes: { mode: Mode; label: string; title: string }[] = [
    { mode: "select", label: "Move", title: "Move tokens and pan the map" },
    { mode: "brush", label: "Brush", title: "Paint cells one by one" },
    { mode: "rect", label: "Rectangle", title: "Paint a rectangle" },
  ];
  return (
    <div className="toolbar" role="toolbar" aria-label="Map tools">
      {modes.map((m) => (
        <button key={m.mode} title={m.title} aria-pressed={props.mode === m.mode} onClick={() => props.setMode(m.mode)}>
          {m.label}
        </button>
      ))}
      {props.mode !== "select" && (
        <>
          <span className="toolbar-sep" />
          {kinds.map((k) => (
            <button key={k} aria-pressed={props.brush === k} onClick={() => props.setBrush(k)}>
              <span className="swatch" style={{ background: TERRAIN[k].fill }} />
              {TERRAIN[k].label}
            </button>
          ))}
          {props.fog && FOG_KINDS.includes(props.brush) && (
            <select
              value={props.fogFor ?? ""}
              onChange={(e) => props.setFogFor(e.target.value || undefined)}
              aria-label="Reveal for"
            >
              <option value="">for the party</option>
              {props.players.map((m) => (
                <option key={m.user_id} value={m.user_id}>
                  for {m.display_name} only
                </option>
              ))}
            </select>
          )}
        </>
      )}
    </div>
  );
}

// drawFog darkens cells the viewer can't see: solid for players, see-through
// for the DM, who needs to see what's underneath.
function drawFog(ctx: Konva.Context, map: MapInfo, revealed: (c: Cell) => boolean, dmView: boolean) {
  ctx.fillStyle = dmView ? "rgba(12,13,17,0.55)" : "rgb(12,13,17)";
  for (let y = 0; y < map.rows; y++) {
    for (let x = 0; x < map.cols; x++) {
      if (!revealed({ x, y })) ctx.fillRect(x * CELL, y * CELL, CELL, CELL);
    }
  }
}

function drawCells(ctx: Konva.Context, cells: [Cell, PaintKind][]) {
  for (const [c, kind] of cells) {
    const style = TERRAIN[kind];
    const x = c.x * CELL;
    const y = c.y * CELL;
    ctx.fillStyle = style.fill;
    ctx.fillRect(x, y, CELL, CELL);
    if (style.hatch) {
      ctx.beginPath();
      for (let i = 1; i < 4; i++) {
        ctx.moveTo(x + (i * CELL) / 4, y);
        ctx.lineTo(x, y + (i * CELL) / 4);
        ctx.moveTo(x + CELL, y + (i * CELL) / 4);
        ctx.lineTo(x + (i * CELL) / 4, y + CELL);
      }
      ctx.strokeStyle = "rgba(90,55,20,0.7)";
      ctx.lineWidth = 2;
      ctx.stroke();
    }
  }
}

function parseKey(key: string): Cell {
  const [x = 0, y = 0] = key.split(",").map(Number);
  return { x, y };
}

// lineCells lists the cells from a to b inclusive, without gaps.
export function lineCells(a: Cell, b: Cell): Cell[] {
  const n = Math.max(Math.abs(b.x - a.x), Math.abs(b.y - a.y));
  const cells: Cell[] = [];
  for (let i = 0; i <= n; i++) {
    const t = n === 0 ? 0 : i / n;
    cells.push({ x: Math.round(a.x + (b.x - a.x) * t), y: Math.round(a.y + (b.y - a.y) * t) });
  }
  return cells;
}

function Grid({ cols, rows }: { cols: number; rows: number }) {
  const lines: number[][] = [];
  for (let x = 0; x <= cols; x++) lines.push([x * CELL, 0, x * CELL, rows * CELL]);
  for (let y = 0; y <= rows; y++) lines.push([0, y * CELL, cols * CELL, y * CELL]);
  return (
    <Group>
      {lines.map((points, i) => (
        // strokeScaleEnabled=false keeps lines one pixel wide at any zoom.
        <Line key={i} points={points} stroke="rgba(0,0,0,0.35)" strokeWidth={1} strokeScaleEnabled={false} perfectDrawEnabled={false} />
      ))}
    </Group>
  );
}

export function initials(label: string): string {
  const words = label.trim().split(/\s+/);
  const letters = words.length > 1 ? words[0]![0]! + words[1]![0]! : label.trim().slice(0, 2);
  return letters.toUpperCase();
}

function clamp(n: number, lo: number, hi: number): number {
  return Math.max(lo, Math.min(hi, n));
}

function useSize(ref: React.RefObject<HTMLElement | null>) {
  const [size, setSize] = useState({ w: 0, h: 0 });
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(([entry]) => {
      if (entry) setSize({ w: entry.contentRect.width, h: entry.contentRect.height });
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);
  return size;
}

function useImage(url: string | undefined): HTMLImageElement | null {
  const [img, setImg] = useState<HTMLImageElement | null>(null);
  useEffect(() => {
    setImg(null);
    if (!url) return;
    const i = new window.Image();
    i.onload = () => setImg(i);
    i.src = url;
    return () => {
      i.onload = null;
    };
  }, [url]);
  return img;
}
