import { useEffect, useRef } from "react";
import * as THREE from "three";

// Sword is a spinning, hovering golden voxel sword for the home page. Drag
// it to spin it; it spins slowly on its own and holds still for people who
// ask for reduced motion.
export default function Sword() {
  const wrap = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const el = wrap.current;
    if (!el) return;

    const renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true });
    renderer.setPixelRatio(Math.min(devicePixelRatio, 2));
    renderer.shadowMap.enabled = true;
    el.appendChild(renderer.domElement);

    const scene = new THREE.Scene();
    const camera = new THREE.PerspectiveCamera(35, 1, 0.1, 100);
    camera.position.set(0, 2.4, 6.6);
    camera.lookAt(0, 1.7, 0);

    const resize = () => {
      const w = el.clientWidth;
      const h = el.clientHeight;
      renderer.setSize(w, h);
      camera.aspect = w / h;
      camera.updateProjectionMatrix();
    };
    const ro = new ResizeObserver(resize);
    ro.observe(el);
    resize();

    // three.js r155+ measures light physically; multiplying by π matches
    // the original r128 look.
    scene.add(new THREE.HemisphereLight(0xffffff, 0x8899aa, 0.72 * Math.PI));
    const sun = new THREE.DirectionalLight(0xffffff, 0.85 * Math.PI);
    sun.position.set(4, 9, 5);
    sun.castShadow = true;
    sun.shadow.mapSize.set(1024, 1024);
    Object.assign(sun.shadow.camera, { left: -3, right: 3, top: 4, bottom: -3 });
    scene.add(sun);

    const ground = new THREE.Mesh(new THREE.PlaneGeometry(12, 12), new THREE.ShadowMaterial({ opacity: 0.22 }));
    ground.rotation.x = -Math.PI / 2;
    ground.receiveShadow = true;
    scene.add(ground);

    const sword = makeSword();
    scene.add(sword.group);

    // Drag to spin, with momentum.
    const canvas = renderer.domElement;
    let drag = false;
    let lastX = 0;
    let vel = 0;
    const down = (e: PointerEvent) => {
      drag = true;
      lastX = e.clientX;
      canvas.setPointerCapture(e.pointerId);
    };
    const move = (e: PointerEvent) => {
      if (!drag) return;
      const d = (e.clientX - lastX) * 0.01;
      lastX = e.clientX;
      vel = d;
      sword.group.rotation.y += d;
    };
    const up = () => {
      drag = false;
    };
    canvas.addEventListener("pointerdown", down);
    canvas.addEventListener("pointermove", move);
    canvas.addEventListener("pointerup", up);
    canvas.addEventListener("pointercancel", up);

    const reduceMotion = matchMedia("(prefers-reduced-motion: reduce)").matches;
    let t = 0;
    let frame = 0;
    const loop = () => {
      frame = requestAnimationFrame(loop);
      if (!reduceMotion) t += 0.016;
      if (!drag) {
        vel *= 0.92;
        sword.group.rotation.y += vel + (reduceMotion ? 0 : 0.008);
      }
      sword.update(t);
      renderer.render(scene, camera);
    };
    loop();

    return () => {
      cancelAnimationFrame(frame);
      ro.disconnect();
      canvas.removeEventListener("pointerdown", down);
      canvas.removeEventListener("pointermove", move);
      canvas.removeEventListener("pointerup", up);
      canvas.removeEventListener("pointercancel", up);
      scene.traverse((o) => {
        if (o instanceof THREE.Mesh) {
          o.geometry.dispose();
          (o.material as THREE.Material).dispose();
        }
      });
      renderer.dispose();
      canvas.remove();
    };
  }, []);

  return <div className="sword" ref={wrap} aria-label="A golden sword, spinning. Drag to turn it." role="img" />;
}

// makeSword builds the sword pixel by pixel on a grid, like a sprite.
function makeSword() {
  // Five-step gold palette, lightest to darkest.
  const G1 = 0xfff1a8, G2 = 0xffd23f, G3 = 0xe8a90e, G4 = 0xb07606, G5 = 0x7a4f06;
  const u = 0.13; // one pixel
  const dz = 0.2; // the sword's thickness

  const mats = new Map<string, THREE.Material>();
  const mat = (c: number, glow: boolean) => {
    const k = `${c}${glow ? "g" : ""}`;
    let m = mats.get(k);
    if (!m) {
      m = glow ? new THREE.MeshBasicMaterial({ color: c }) : new THREE.MeshLambertMaterial({ color: c });
      mats.set(k, m);
    }
    return m;
  };
  const geo = new THREE.BoxGeometry(1, 1, 1);
  const group = new THREE.Group();
  const box = (w: number, h: number, d: number, c: number, x: number, y: number, z: number, glow = false) => {
    const o = new THREE.Mesh(geo, mat(c, glow));
    o.scale.set(w, h, d);
    o.position.set(x, y + h / 2, z);
    o.castShadow = !glow;
    o.receiveShadow = true;
    group.add(o);
    return o;
  };
  const px = (c: number, col: number, row: number) => box(u, u, dz, c, col * u, row * u, 0);

  // Pommel (rows 0-2)
  for (let r = 0; r < 3; r++) for (let c = -1; c <= 1; c++) px(c === 0 ? G3 : G4, c, r);
  px(G2, 0, 0);

  // Grip (rows 3-7): a darker core with wrap bands
  for (let r = 3; r < 8; r++) {
    px(G4, -1, r);
    px(G5, 0, r);
    px(G4, 1, r);
  }
  for (let r = 4; r < 8; r += 2) px(G3, 0, r);

  // Crossguard (rows 8-9)
  for (let c = -4; c <= 4; c++) {
    px(Math.abs(c) === 4 ? G4 : G3, c, 8);
    px(Math.abs(c) === 4 ? G3 : G2, c, 9);
  }
  px(G1, -3, 9);
  px(G1, 3, 9);

  // Blade (rows 10-21): a light left edge and dark right edge fake a sheen
  for (let r = 10; r < 22; r++) {
    px(G1, -1, r);
    px(G2, 0, r);
    px(G3, 1, r);
  }
  for (let r = 12; r < 21; r += 3) px(G1, 0, r);

  // Tip (rows 22-24)
  px(G1, -1, 22);
  px(G2, 0, 22);
  px(G3, 1, 22);
  px(G1, 0, 23);
  px(G2, 0, 24);

  // A sparkle that travels up the blade
  const sparkle = box(u * 0.6, u * 0.6, u * 0.6, 0xffffff, -2 * u, 19 * u, 0.1, true);

  function update(t: number) {
    group.position.y = 0.15 + Math.sin(t * 1.6) * 0.08; // a gentle hover
    const s = Math.max(0, Math.sin(t * 2.5));
    sparkle.scale.setScalar(s * u * 0.7 + 0.001);
    sparkle.position.y = (10 + ((t * 4) % 12)) * u;
  }

  return { group, update };
}
