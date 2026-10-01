import { Room } from 'livekit-client';

const room = new Room({ adaptiveStream: true, dynacast: true });
const token = 'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3NzkyNzk2MDksImlzcyI6ImRldmtleSIsIm5iZiI6MTc3OTI3OTMwOSwic3ViIjoiNzEyMjc3MDU4MzI3Mjg1NzYwIiwidmlkZW8iOnsiY2FuUHVibGlzaCI6dHJ1ZSwiY2FuUHVibGlzaERhdGEiOnRydWUsImNhblN1YnNjcmliZSI6dHJ1ZSwicm9vbSI6InJydC1jaGFubmVsLTcxMTYzNjcwMzAzNDYwOTY2NSIsInJvb21Kb2luIjp0cnVlfX0.05OShbzU04qsiat1FAqqz0VDfaX4jp5e24WQNRhtsnA';

room.on('connected', () => console.log('CONNECTED'));
room.on('disconnected', (reason) => console.log('DISCONNECTED:', reason));
room.on('connectionStateChanged', (state) => console.log('STATE:', state));

try {
  await room.connect('ws://localhost:7880', token);
  console.log('CONNECT SUCCESS');
  await new Promise(r => setTimeout(r, 2000));
  await room.disconnect();
  console.log('MANUAL DISCONNECT');
} catch (err) {
  console.error('CONNECT FAILED:', err.message);
}
