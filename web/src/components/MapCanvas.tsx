import type Konva from "konva";
import { useEffect, useRef, useState } from "react";
import { Circle, Group, Image as KImage, Layer, Line, Rect, Stage, Text } from "react-konva";
import { tokenPos, type TableState } from "../store";
import type { Cell, Token, TokenID, UserID } from "../types";

const CELL = 64; // px per grid cell at zoom 1
const MIN_ZOOM = 0.1;
const MAX_ZOOM = 5;

interface Props {
  table: TableState;
  me: UserID;
  isDM: boolean;
  selected: TokenID | null;
  onSelect(id: TokenID | null): void;
  // onMove returns false if the move could not be sent.
  onMove(id: TokenID, to: Cell): boolean;
}

export function MapCanvas({ table, me, isDM, selected, onSelect, onMove }: Props) {
  const wrap = useRef<HTMLDivElement>(null);
  const size = useSize(wrap);
  const map = table.game?.map ?? null;
  const image = useImage(map?.image_url);
  const [view, setView] = useState({ x: 0, y: 0, scale: 1 });

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

  return (
    <div className="canvas" ref={wrap}>
      {!map && (
        <div className="canvas-empty">{isDM ? "Upload a map to start." : "Waiting for the DM to upload a map…"}</div>
      )}
      {map && hasSize && (
        <Stage
          width={size.w}
          height={size.h}
          x={view.x}
          y={view.y}
          scaleX={view.scale}
          scaleY={view.scale}
          draggable
          onWheel={zoom}
          onDragEnd={(e) => {
            if (e.target === e.target.getStage()) setView((v) => ({ ...v, x: e.target.x(), y: e.target.y() }));
          }}
          onMouseDown={(e) => {
            if (e.target === e.target.getStage() || e.target.name() === "background") onSelect(null);
          }}
          onTouchStart={(e) => {
            if (e.target === e.target.getStage() || e.target.name() === "background") onSelect(null);
          }}
        >
          <Layer>
            {image ? (
              <KImage name="background" image={image} width={map.cols * CELL} height={map.rows * CELL} />
            ) : (
              <Rect name="background" width={map.cols * CELL} height={map.rows * CELL} fill="#2b2f36" />
            )}
          </Layer>
          <Layer listening={false}>
            <Grid cols={map.cols} rows={map.rows} />
          </Layer>
          <Layer>
            {Object.values(table.game!.tokens).map((t) => {
              const pos = tokenPos(table, t.id) ?? t.pos;
              const mine = t.controllers.includes(me);
              const px = t.size * CELL;
              return (
                <Group
                  key={t.id}
                  x={pos.x * CELL}
                  y={pos.y * CELL}
                  draggable={isDM || mine}
                  onMouseDown={() => onSelect(t.id)}
                  onTouchStart={() => onSelect(t.id)}
                  onDragEnd={(e) => {
                    e.cancelBubble = true; // don't pan the stage
                    dropToken(t, pos, e.target);
                  }}
                >
                  <Circle
                    x={px / 2}
                    y={px / 2}
                    radius={px / 2 - 4}
                    fill={t.color}
                    stroke={selected === t.id ? "#ffffff" : mine ? "#ffd166" : "#111111"}
                    strokeWidth={selected === t.id ? 4 : 2}
                    shadowColor="black"
                    shadowBlur={6}
                    shadowOpacity={0.5}
                  />
                  <Text
                    text={initials(t.label)}
                    width={px}
                    height={px}
                    align="center"
                    verticalAlign="middle"
                    fontSize={px * 0.32}
                    fontStyle="bold"
                    fill="#ffffff"
                    listening={false}
                  />
                  <Text
                    text={t.label}
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
          </Layer>
        </Stage>
      )}
    </div>
  );
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
