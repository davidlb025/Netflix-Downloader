#ifdef _WIN32
#include <windows.h>
#include <objbase.h>
using PathChar = wchar_t;
#else
#include <dlfcn.h>
using PathChar = char;
#endif
#include "include/content_decryption_module.h"
#include <algorithm>
#include <chrono>
#include <cstdint>
#include <fstream>
#include <iostream>
#include <iterator>
#include <string>
#include <thread>
#include <vector>
#include <cstring>
#include <functional>
#include <sstream>
#include <filesystem>
#include <memory>
#include <set>
#include <random>
#include <array>
#include <cstdio>
#ifdef _WIN32
#include <io.h>
#include <fcntl.h>
#endif

// A new host for the public Chromium CDM interface. It does not patch a CDM,
// impersonate a signed browser host, or export device/content private keys.
using Clock = std::chrono::steady_clock;

using PathString = std::basic_string<PathChar>;

static std::vector<uint8_t> readFile(const PathString& path) {
  std::ifstream stream(path.c_str(), std::ios::binary);
  if (!stream) return {};
  return {std::istreambuf_iterator<char>(stream), {}};
}

class Buffer final : public cdm::Buffer {
 public:
  explicit Buffer(uint32_t capacity) : bytes(capacity) {}
  void Destroy() override { delete this; }
  uint32_t Capacity() const override { return uint32_t(bytes.size()); }
  uint8_t* Data() override { return bytes.data(); }
  void SetSize(uint32_t value) override { used = std::min(value, Capacity()); }
  uint32_t Size() const override { return used; }
 private:
  std::vector<uint8_t> bytes;
  uint32_t used = 0;
};

class Block final : public cdm::DecryptedBlock {
 public:
  ~Block() override { if (buffer) buffer->Destroy(); }
  void SetDecryptedBuffer(cdm::Buffer* value) override { buffer=value; }
  cdm::Buffer* DecryptedBuffer() override { return buffer; }
  void SetTimestamp(int64_t value) override { timestamp=value; }
  int64_t Timestamp() const override { return timestamp; }
 private:
  cdm::Buffer* buffer=nullptr;
  int64_t timestamp=0;
};

class Frame final : public cdm::VideoFrame {
 public:
  ~Frame()override{if(buffer)buffer->Destroy();}
  void SetFormat(cdm::VideoFormat value)override{format=value;}
  cdm::VideoFormat Format()const override{return format;}
  void SetSize(cdm::Size value)override{size=value;}
  cdm::Size Size()const override{return size;}
  void SetFrameBuffer(cdm::Buffer* value)override{buffer=value;}
  cdm::Buffer* FrameBuffer()override{return buffer;}
  void SetPlaneOffset(cdm::VideoPlane plane,uint32_t value)override{if(plane<3)offsets[plane]=value;}
  uint32_t PlaneOffset(cdm::VideoPlane plane)override{return plane<3?offsets[plane]:0;}
  void SetStride(cdm::VideoPlane plane,uint32_t value)override{if(plane<3)strides[plane]=value;}
  uint32_t Stride(cdm::VideoPlane plane)override{return plane<3?strides[plane]:0;}
  void SetTimestamp(int64_t value)override{timestamp=value;}
  int64_t Timestamp()const override{return timestamp;}
 private:
  cdm::VideoFormat format=cdm::kUnknownVideoFormat;cdm::Size size{};cdm::Buffer* buffer=nullptr;uint32_t offsets[3]{},strides[3]{};int64_t timestamp=0;
};

static uint32_t read32(std::istream& input) {
  unsigned char bytes[4]{};input.read(reinterpret_cast<char*>(bytes),4);
  return uint32_t(bytes[0]) | uint32_t(bytes[1])<<8 | uint32_t(bytes[2])<<16 | uint32_t(bytes[3])<<24;
}
static uint64_t read64(std::istream& input) { auto low=read32(input);return uint64_t(low)|(uint64_t(read32(input))<<32); }
static void write32(std::ostream& output,uint32_t value) { unsigned char bytes[4];for(int i=0;i<4;i++)bytes[i]=uint8_t(value>>(8*i));output.write(reinterpret_cast<char*>(bytes),4); }
static void write64(std::ostream& output,uint64_t value) { write32(output,uint32_t(value));write32(output,uint32_t(value>>32)); }

static std::vector<uint8_t> annexBConfiguration(const std::vector<uint8_t>& avcc) {
  if(avcc.size()<7||avcc[0]!=1)return {};
  std::vector<uint8_t> output;size_t position=6;
  for(int group=0;group<2;group++){
    unsigned count=group==0?(avcc[5]&31):(position<avcc.size()?avcc[position++]:0);
    for(unsigned i=0;i<count;i++){if(position+2>avcc.size())return {};auto size=(unsigned(avcc[position])<<8)|avcc[position+1];position+=2;if(size==0||size>avcc.size()-position)return {};output.insert(output.end(),{0,0,0,1});output.insert(output.end(),avcc.begin()+position,avcc.begin()+position+size);position+=size;}
  }
  return output;
}

class Y4MOutput {
 public:
  uint32_t rateNum=0,rateDen=0,frames=0;int32_t width=0,height=0;int64_t lastTimestamp=-1;
  bool write(Frame& frame) {
    auto* buffer=frame.FrameBuffer();auto size=frame.Size();
    if(!buffer||frame.Format()!=cdm::kI420||size.width<=0||size.height<=0||size.width>8192||size.height>8192||rateNum==0||rateDen==0)return false;
    if(frames==0){width=size.width;height=size.height;std::ostringstream header;header<<"YUV4MPEG2 W"<<width<<" H"<<height<<" F"<<rateNum<<":"<<rateDen<<" Ip A0:0 C420jpeg\n";auto value=header.str();if(std::fwrite(value.data(),1,value.size(),stdout)!=value.size())return false;}
    if(size.width!=width||size.height!=height||(frames>0&&frame.Timestamp()<=lastTimestamp))return false;
    if(std::fwrite("FRAME\n",1,6,stdout)!=6)return false;
    for(uint32_t plane=0;plane<3;plane++){uint32_t rows=plane==0?uint32_t(height):uint32_t((height+1)/2);uint32_t columns=plane==0?uint32_t(width):uint32_t((width+1)/2);uint32_t offset=frame.PlaneOffset(plane),stride=frame.Stride(plane);if(stride<columns||uint64_t(offset)+uint64_t(rows-1)*stride+columns>buffer->Capacity())return false;for(uint32_t row=0;row<rows;row++)if(std::fwrite(buffer->Data()+offset+uint64_t(row)*stride,1,columns,stdout)!=columns)return false;}
    ++frames;lastTimestamp=frame.Timestamp();return true;
  }
};

static bool decryptJob(cdm::ContentDecryptionModule_10* module,const std::vector<std::vector<uint8_t>>& keyIDs,const std::function<void()>& dispatch,const PathString& jobPath,const PathString& outputPath,bool render) {
  std::ifstream input(jobPath.c_str(),std::ios::binary);
  std::ofstream output(outputPath.c_str(),std::ios::binary);
  char magic[8]{};input.read(magic,8);
  if (!input || !output || std::memcmp(magic,"SFCENC05",8)!=0)return false;
  uint32_t count=read32(input);if(count>10000000)return false;
  auto mode=read32(input);if(mode>1)return false;uint32_t decodedFrames=0,protectedSamples=0;
  auto configurationSize=read32(input);if(configurationSize>1024*1024)return false;
  std::vector<uint8_t> configuration(configurationSize);input.read(reinterpret_cast<char*>(configuration.data()),configurationSize);if(!input)return false;
  struct DecoderGuard{cdm::ContentDecryptionModule_10* module;bool initialized=false;~DecoderGuard(){if(initialized)module->DeinitializeDecoder(cdm::kStreamTypeVideo);}} decoder{module};
  std::vector<uint8_t> extra,decoderExtra;
  Y4MOutput videoOutput;
  if(configurationSize>0){
    std::istringstream values(std::string(configuration.begin(),configuration.end()));auto width=read32(values);auto height=read32(values);auto profile=read32(values);videoOutput.rateNum=read32(values);videoOutput.rateDen=read32(values);auto extraSize=read32(values);if(!values||configurationSize<24||width==0||height==0||width>8192||height>8192||extraSize>configurationSize-24)return false;extra.resize(extraSize);values.read(reinterpret_cast<char*>(extra.data()),extraSize);if(!values)return false;
    decoderExtra=extra;if(mode==1){if(extra.size()<5||(extra[4]&3)!=3||annexBConfiguration(extra).empty())return false;}
    cdm::VideoDecoderConfig_2 config{};config.codec=cdm::kCodecH264;config.profile=static_cast<cdm::VideoCodecProfile>(profile);config.format=cdm::kI420;config.coded_size={int32_t(width),int32_t(height)};config.extra_data=decoderExtra.data();config.extra_data_size=uint32_t(decoderExtra.size());config.encryption_scheme=cdm::EncryptionScheme::kCenc;
    auto status=module->InitializeVideoDecoder(config);decoder.initialized=status==cdm::kSuccess;std::cout<<"{\"event\":\"decoderInitialized\",\"status\":"<<status<<",\"width\":"<<width<<",\"height\":"<<height<<"}"<<std::endl;
  }
  output.write("SFCLEAR1",8);write32(output,count);
  for(uint32_t i=0;i<count;i++) {
    dispatch();
    auto file=read32(input);auto offset=read64(input);auto size=read32(input);auto ivSize=read32(input);auto subCount=read32(input);auto scheme=read32(input);auto crypt=read32(input);auto skip=read32(input);
    auto timestamp=int64_t(read64(input));
    if(!input || size==0 || size>32*1024*1024 || (scheme!=0&&ivSize!=8&&ivSize!=16) || (scheme==0&&ivSize!=0) || subCount>65535 || scheme>2)return false;
    uint8_t kid[16],iv[16]{};input.read(reinterpret_cast<char*>(kid),16);input.read(reinterpret_cast<char*>(iv),ivSize);
    std::vector<cdm::SubsampleEntry> subs(subCount);uint64_t sum=0;
    for(auto& sub:subs){sub.clear_bytes=read32(input);sub.cipher_bytes=read32(input);sum+=uint64_t(sub.clear_bytes)+sub.cipher_bytes;}
    if(subCount && sum!=size)return false;
    std::vector<uint8_t> data(size);input.read(reinterpret_cast<char*>(data.data()),size);if(!input)return false;
    if(mode==1){
      for(size_t position=0;position<data.size();){if(data.size()-position<4)return false;size_t segment=0;bool isClear=scheme==0;for(auto sub:subs){if(position>=segment && position+4<=segment+sub.clear_bytes){isClear=true;break;}segment+=size_t(sub.clear_bytes)+sub.cipher_bytes;}if(!isClear)return false;uint32_t length=(uint32_t(data[position])<<24)|(uint32_t(data[position+1])<<16)|(uint32_t(data[position+2])<<8)|data[position+3];if(length==0||length>data.size()-position-4)return false;data[position]=data[position+1]=data[position+2]=0;data[position+3]=1;position+=4+length;}
      // The H.264 decoder consumes Annex B access units. Its first input must
      // include SPS/PPS, as with Chromium's MP4 bitstream converter. The avcC
      // decoder configuration alone does not supply these in this CDM build.
      if(i==0){auto prefix=annexBConfiguration(extra);if(prefix.empty())return false;if(scheme!=0){if(subs.empty())return false;subs[0].clear_bytes+=uint32_t(prefix.size());}prefix.insert(prefix.end(),data.begin(),data.end());data=std::move(prefix);size=uint32_t(data.size());}
    }
    if(i<3){bool match=std::any_of(keyIDs.begin(),keyIDs.end(),[&](const auto& id){return id.size()==16&&std::memcmp(id.data(),kid,16)==0;});uint64_t cipherBytes=0;for(auto sub:subs)cipherBytes+=sub.cipher_bytes;std::cout<<"{\"event\":\"sampleInput\",\"index\":"<<i<<",\"keyMatchesLicense\":"<<(match?"true":"false")<<",\"size\":"<<size<<",\"subsamples\":"<<subCount<<",\"cipherBytes\":"<<cipherBytes<<"}"<<std::endl;}
    // CENC defines one logical CTR stream formed by the cipher regions, with
    // clear regions excluded. Gather that stream, then restore its positions.
    std::vector<uint8_t> cipherData;
    bool gather=scheme==1 && mode==0;
    if(gather){if(subCount==0)cipherData=data;else{size_t position=0;for(auto sub:subs){position+=sub.clear_bytes;cipherData.insert(cipherData.end(),data.begin()+position,data.begin()+position+sub.cipher_bytes);position+=sub.cipher_bytes;}}}
    if(gather && cipherData.empty()){write32(output,file);write64(output,offset);write32(output,size);output.write(reinterpret_cast<const char*>(data.data()),size);if(!output)return false;continue;}
    // CTR's plaintext prefix is unchanged by appending bytes. Supply complete
    // AES blocks to the CDM, and discard padding when restoring the sample.
    if(gather)cipherData.resize((cipherData.size()+15)&~size_t(15),0);
    // An 8-byte CENC IV is the high half of the 16-byte AES counter.
    cdm::InputBuffer_2 encrypted{};encrypted.data=gather?cipherData.data():data.data();encrypted.data_size=gather?uint32_t(cipherData.size()):size;encrypted.encryption_scheme=static_cast<cdm::EncryptionScheme>(scheme);encrypted.key_id=kid;encrypted.key_id_size=16;encrypted.iv=iv;encrypted.iv_size=16;encrypted.subsamples=gather?nullptr:subs.data();encrypted.num_subsamples=gather?0:subCount;encrypted.pattern={crypt,skip};encrypted.timestamp=timestamp;
    if(scheme==0){encrypted.key_id=nullptr;encrypted.key_id_size=0;encrypted.iv=nullptr;encrypted.iv_size=0;encrypted.subsamples=nullptr;encrypted.num_subsamples=0;}
    if(mode==1){
      if(scheme!=0)++protectedSamples;
      if(!decoder.initialized)return false;
      Frame frame;auto status=module->DecryptAndDecodeFrame(encrypted,&frame);
      if(status==cdm::kNeedMoreData)continue;
      if(status!=cdm::kSuccess||!frame.FrameBuffer()){std::cout<<"{\"event\":\"decodeFailed\",\"sample\":"<<i<<",\"status\":"<<status<<"}"<<std::endl;return false;}
      if(render&&!videoOutput.write(frame))return false;
      ++decodedFrames;if(decodedFrames<=3)std::cout<<"{\"event\":\"frameDecoded\",\"width\":"<<frame.Size().width<<",\"height\":"<<frame.Size().height<<"}"<<std::endl;
      if((i+1)%5000==0)std::cout<<"{\"event\":\"decodeProgress\",\"done\":"<<(i+1)<<",\"total\":"<<count<<"}"<<std::endl;
      continue;
    }
    Block block;
    auto status=module->Decrypt(encrypted,&block);
    for(int retry=0;status==cdm::kNoKey && retry<3;retry++){std::this_thread::sleep_for(std::chrono::milliseconds(10));dispatch();status=module->Decrypt(encrypted,&block);}
    if(status!=cdm::kSuccess || !block.DecryptedBuffer() || block.DecryptedBuffer()->Size()!=encrypted.data_size) {
      std::cout<<"{\"event\":\"decryptFailed\",\"sample\":"<<i<<",\"status\":"<<status<<",\"cipherBytes\":"<<encrypted.data_size<<"}"<<std::endl;return false;
    }
    const uint8_t* clear=block.DecryptedBuffer()->Data();
    std::vector<uint8_t> restored;
    if(gather){restored=data;if(subCount==0)std::memcpy(restored.data(),clear,size);else{size_t position=0,cipherPosition=0;for(auto sub:subs){position+=sub.clear_bytes;std::memcpy(restored.data()+position,clear+cipherPosition,sub.cipher_bytes);position+=sub.cipher_bytes;cipherPosition+=sub.cipher_bytes;}}clear=restored.data();}
    if(i<3)std::cout<<"{\"event\":\"sampleDecrypted\",\"index\":"<<i<<",\"changed\":"<<(std::memcmp(clear,data.data(),size)!=0?"true":"false")<<"}"<<std::endl;
    write32(output,file);write64(output,offset);write32(output,size);output.write(reinterpret_cast<const char*>(clear),size);
    if(!output)return false;
    if((i+1)%5000==0)std::cout<<"{\"event\":\"decryptProgress\",\"done\":"<<(i+1)<<",\"total\":"<<count<<"}"<<std::endl;
  }
  if(input.peek()!=std::char_traits<char>::eof())return false;
  if(mode==1){
    bool drained=false;for(int pending=0;pending<32;pending++){dispatch();cdm::InputBuffer_2 end{};Frame frame;auto status=module->DecryptAndDecodeFrame(end,&frame);if(status==cdm::kNeedMoreData){drained=true;break;}if(status!=cdm::kSuccess||!frame.FrameBuffer()||(render&&!videoOutput.write(frame)))return false;++decodedFrames;}
    if(!drained||(render&&decodedFrames!=count))return false;
    if(render&&std::fflush(stdout)!=0)return false;
  }
  output.close();
  if(mode==1)std::cout<<"{\"event\":\"decodeCheckDone\",\"frames\":"<<decodedFrames<<",\"protectedSamples\":"<<protectedSamples<<"}"<<std::endl;
  else std::cout<<"{\"event\":\"decryptionDone\",\"samples\":"<<count<<"}"<<std::endl;
  return bool(output) && (mode==0||(decodedFrames>0&&protectedSamples>0));
}

class Host final : public cdm::Host_10 {
 public:
  cdm::ContentDecryptionModule_10* module = nullptr;
  bool initialized = false, rejected = false, certificateReady = false;
  std::string session;
  std::vector<uint8_t> message;
  uint32_t usableKeys = 0;
  std::vector<std::vector<uint8_t>> keyIDs;
  struct Timer { Clock::time_point due; void* context; };
  std::vector<Timer> timers;
  std::vector<std::function<void()>> tasks;
  std::filesystem::path storageRoot;
  std::array<uint8_t,32> storageID{};
  std::set<std::string> openFiles;

  bool configureStorage(const std::filesystem::path& folder){
    try{storageRoot=folder;std::filesystem::create_directories(folder);std::ifstream input(folder/"storage.id",std::ios::binary);input.read(reinterpret_cast<char*>(storageID.data()),storageID.size());if(!input){std::random_device random;for(auto& byte:storageID)byte=uint8_t(random());std::ofstream output(folder/"storage.id",std::ios::binary);output.write(reinterpret_cast<const char*>(storageID.data()),storageID.size());if(!output)return false;}}
    catch(...){return false;}return true;
  }

  cdm::Buffer* Allocate(uint32_t capacity) override {
    if (capacity > 64 * 1024 * 1024) return nullptr;
    return new Buffer(capacity);
  }
  void SetTimer(int64_t delay, void* context) override {
    timers.push_back({Clock::now() + std::chrono::milliseconds(std::max<int64_t>(0, delay)), context});
  }
  cdm::Time GetCurrentWallTime() override {
    return std::chrono::duration<double>(std::chrono::system_clock::now().time_since_epoch()).count();
  }
  void OnInitialized(bool success) override {
    initialized = success;
    std::cout << "{\"event\":\"initialized\",\"success\":" << (success ? "true" : "false") << "}" << std::endl;
  }
  void OnResolveKeyStatusPromise(uint32_t promise, cdm::KeyStatus status) override {
    std::cout << "{\"event\":\"policy\",\"promise\":" << promise << ",\"status\":" << status << "}" << std::endl;
  }
  void OnResolveNewSessionPromise(uint32_t, const char* value, uint32_t size) override {
    session.assign(value ? value : "", value ? size : 0);
    std::cout << "{\"event\":\"session\",\"idBytes\":" << session.size() << "}" << std::endl;
  }
  void OnResolvePromise(uint32_t promise) override {
    if (promise == 4) certificateReady = true;
    std::cout << "{\"event\":\"resolved\",\"promise\":" << promise << "}" << std::endl;
  }
  void OnRejectPromise(uint32_t promise, cdm::Exception exception, uint32_t code, const char*, uint32_t size) override {
    rejected = true;
    std::cout << "{\"event\":\"rejected\",\"promise\":" << promise << ",\"exception\":" << exception << ",\"systemCode\":" << code << ",\"detailBytes\":" << size << "}" << std::endl;
  }
  void OnSessionMessage(const char*, uint32_t, cdm::MessageType type, const char* bytes, uint32_t size) override {
    if (size > 16 * 1024 * 1024) { rejected = true; return; }
    message.assign(bytes, bytes + size);
    std::cout << "{\"event\":\"message\",\"type\":" << type << ",\"bytes\":" << size << "}" << std::endl;
  }
  void OnSessionKeysChange(const char*, uint32_t, bool, const cdm::KeyInformation* keys, uint32_t count) override {
    usableKeys = 0;
    keyIDs.clear();for(uint32_t i=0;i<count;i++)if(keys[i].key_id_size<=64)keyIDs.emplace_back(keys[i].key_id,keys[i].key_id+keys[i].key_id_size);
    for (uint32_t i = 0; i < count; ++i) if (keys[i].status == cdm::kUsable) ++usableKeys;
    std::cout << "{\"event\":\"keysChanged\",\"count\":" << count << ",\"usable\":" << usableKeys << "}" << std::endl;
    for(uint32_t i=0;i<count && i<10;i++)std::cout<<"{\"event\":\"keyStatus\",\"status\":"<<keys[i].status<<",\"systemCode\":"<<keys[i].system_code<<"}"<<std::endl;
  }
  void OnExpirationChange(const char*, uint32_t, cdm::Time) override {}
  void OnSessionClosed(const char*, uint32_t) override { session.clear(); }
  void SendPlatformChallenge(const char*, uint32_t, const char*, uint32_t) override {
    tasks.push_back([this]{cdm::PlatformChallengeResponse unavailable{};module->OnPlatformChallengeResponse(unavailable);});
  }
  void EnableOutputProtection(uint32_t) override {}
  void QueryOutputProtectionStatus() override {
    // This worker creates no audio/video output or display connection. It
    // reports no attached output links and does not claim HDCP protection.
    tasks.push_back([this]{std::cout<<"{\"event\":\"outputProtectionQuery\",\"headless\":true,\"links\":0,\"protection\":0}"<<std::endl;module->OnQueryOutputProtectionStatus(cdm::kQuerySucceeded,0,0);});
  }
  void OnDeferredInitializationDone(cdm::StreamType, cdm::Status) override {}
  cdm::FileIO* CreateFileIO(cdm::FileIOClient*) override;
  void RequestStorageId(uint32_t version) override { tasks.push_back([this,version]{if(version<=1)module->OnStorageId(1,storageID.data(),storageID.size());else module->OnStorageId(version,nullptr,0);}); }

  void pump(bool pause=true) {
    auto pending=std::move(tasks);tasks.clear();for(auto& task:pending)task();
    std::vector<void*> ready;
    for (auto it = timers.begin(); it != timers.end();) {
      if (it->due <= Clock::now()) { ready.push_back(it->context); it = timers.erase(it); }
      else ++it;
    }
    for (void* context : ready) module->TimerExpired(context);
    if(pause)std::this_thread::sleep_for(std::chrono::milliseconds(10));
  }
};

class PrivateFile final : public cdm::FileIO {
 public:
  PrivateFile(Host* owner,cdm::FileIOClient* client):host(owner),state(std::make_shared<State>()){state->client=client;}
  void Open(const char* bytes,uint32_t count)override{
    std::string name(bytes,count);bool valid=count>0&&count<=256&&name[0]!='_'&&name!="."&&name!="..";
    for(unsigned char ch:name)if(!((ch>='a'&&ch<='z')||(ch>='A'&&ch<='Z')||(ch>='0'&&ch<='9')||ch=='.'||ch=='_'||ch=='-'))valid=false;
    auto result=cdm::FileIOClient::Status::kError;
    if(valid&&!state->opened){if(host->openFiles.count(name))result=cdm::FileIOClient::Status::kInUse;else{state->name=name;state->path=host->storageRoot/name;state->opened=true;host->openFiles.insert(name);result=cdm::FileIOClient::Status::kSuccess;}}
    auto shared=state;host->tasks.push_back([shared,result]{if(!shared->closed)shared->client->OnOpenComplete(result);});
  }
  void Read()override{
    auto shared=state;std::vector<uint8_t> data;auto result=cdm::FileIOClient::Status::kError;
    try{if(state->opened){if(!std::filesystem::exists(state->path))result=cdm::FileIOClient::Status::kSuccess;else if(std::filesystem::is_regular_file(state->path)&&std::filesystem::file_size(state->path)<=16*1024*1024){std::ifstream input(state->path,std::ios::binary);data.assign(std::istreambuf_iterator<char>(input),{});if(!input.bad())result=cdm::FileIOClient::Status::kSuccess;}}}catch(...){}
    host->tasks.push_back([shared,result,data=std::move(data)]{if(!shared->closed)shared->client->OnReadComplete(result,data.data(),uint32_t(data.size()));});
  }
  void Write(const uint8_t* bytes,uint32_t count)override{
    auto shared=state;auto result=cdm::FileIOClient::Status::kError;
    try{if(state->opened&&count<=16*1024*1024){auto pending=state->path;pending+=".pending";std::ofstream output(pending,std::ios::binary);if(count)output.write(reinterpret_cast<const char*>(bytes),count);output.close();if(output){
#ifdef _WIN32
      if(MoveFileExW(pending.c_str(),state->path.c_str(),MOVEFILE_REPLACE_EXISTING|MOVEFILE_WRITE_THROUGH))result=cdm::FileIOClient::Status::kSuccess;
#else
      std::filesystem::rename(pending,state->path);result=cdm::FileIOClient::Status::kSuccess;
#endif
    }}}catch(...){}
    host->tasks.push_back([shared,result]{if(!shared->closed)shared->client->OnWriteComplete(result);});
  }
  void Close()override{state->closed=true;if(state->opened)host->openFiles.erase(state->name);delete this;}
 private:
  struct State{cdm::FileIOClient* client=nullptr;std::filesystem::path path;std::string name;bool opened=false,closed=false;};
  Host* host;std::shared_ptr<State> state;
};

cdm::FileIO* Host::CreateFileIO(cdm::FileIOClient* client){if(storageRoot.empty()||!client)return nullptr;return new PrivateFile(this,client);}

static void* getHost(int version, void* data) {
  return version == 10 ? static_cast<cdm::Host_10*>(static_cast<Host*>(data)) : nullptr;
}

#ifdef _WIN32
int wmain(int argc, wchar_t** argv) {
#else
int main(int argc, char** argv) {
#endif
  bool render=argc==9&&PathString(argv[8])==PathString{
#ifdef _WIN32
    L"--y4m-stdout"
#else
    "--y4m-stdout"
#endif
  };
  if(render){std::cout.rdbuf(std::cerr.rdbuf());
#ifdef _WIN32
    _setmode(_fileno(stdout),_O_BINARY);
#endif
  }
  if (argc < 4 || (argc > 6 && argc != 8 && !render)) {
    std::cerr << "Usage: cdm-probe <official-cdm.dll> <pssh.bin> <challenge-output.bin> [license-response.bin] [service-certificate.bin]\n";
    return 2;
  }
#ifdef _WIN32
  SetErrorMode(SEM_FAILCRITICALERRORS | SEM_NOGPFAULTERRORBOX);
  CoInitializeEx(nullptr, COINIT_MULTITHREADED);
#endif
  auto pssh = readFile(argv[2]);
  if (pssh.empty() || pssh.size() > 1024 * 1024) return 3;
#ifdef _WIN32
  HMODULE library = LoadLibraryExW(argv[1], nullptr, LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR | LOAD_LIBRARY_SEARCH_DEFAULT_DIRS);
  if (!library) { std::cout << "{\"event\":\"loadFailed\",\"code\":" << GetLastError() << "}" << std::endl; return 4; }
#else
  auto library = dlopen(argv[1], RTLD_NOW | RTLD_LOCAL);
  if (!library) { std::cout << "{\"event\":\"loadFailed\"}" << std::endl; return 4; }
#define GetProcAddress dlsym
#endif
  auto initialize = reinterpret_cast<void(*)()>(GetProcAddress(library, "InitializeCdmModule_4"));
  auto deinitialize = reinterpret_cast<void(*)()>(GetProcAddress(library, "DeinitializeCdmModule"));
  auto create = reinterpret_cast<decltype(&CreateCdmInstance)>(GetProcAddress(library, "CreateCdmInstance"));
  auto version = reinterpret_cast<const char*(*)()>(GetProcAddress(library, "GetCdmVersion"));
  if (!initialize || !create || !version || !deinitialize) return 5;
  std::cout << "{\"event\":\"module\",\"version\":\"" << version() << "\",\"interface\":10}" << std::endl;
  initialize();
  Host host;
  if(!host.configureStorage(std::filesystem::path(argv[3]).parent_path()/"cdm-state")){std::cout<<"{\"event\":\"storageInitializeFailed\"}"<<std::endl;return 12;}
  host.module = static_cast<cdm::ContentDecryptionModule_10*>(create(10, "com.widevine.alpha", 18, getHost, &host));
  if (!host.module) { std::cout << "{\"event\":\"instanceRejected\"}" << std::endl; return 6; }
  host.module->Initialize(false, true, false);
  auto until = Clock::now() + std::chrono::seconds(8);
  while (!host.initialized && !host.rejected && Clock::now() < until) host.pump();
  int result = 7;
  if (host.initialized && argc >= 6 && argv[5][0]) {
    auto certificate = readFile(argv[5]);
    if (certificate.empty() || certificate.size() > 1024 * 1024) host.rejected = true;
    else {
      host.module->SetServerCertificate(4, certificate.data(), uint32_t(certificate.size()));
      until = Clock::now() + std::chrono::seconds(5);
      while (!host.certificateReady && !host.rejected && Clock::now() < until) host.pump();
      if (!host.certificateReady) host.rejected = true;
    }
  }
  if (host.initialized && !host.rejected) {
    host.module->CreateSessionAndGenerateRequest(1, cdm::kTemporary, cdm::kCenc, pssh.data(), uint32_t(pssh.size()));
    until = Clock::now() + std::chrono::seconds(10);
    while (host.message.empty() && !host.rejected && Clock::now() < until) host.pump();
    if (!host.message.empty()) {
      std::ofstream output(argv[3], std::ios::binary);
      output.write(reinterpret_cast<const char*>(host.message.data()), host.message.size());
      output.close();
      result = output ? 0 : 8;
      PathString sessionPath(argv[3]);
      for (char ch : std::string(".session")) sessionPath.push_back(PathChar(ch));
      std::ofstream sessionOutput(sessionPath.c_str(), std::ios::binary);
      sessionOutput.write(host.session.data(), host.session.size());
      sessionOutput.close();
      if (!sessionOutput) result = 8;
      if (result == 0) std::cout << "{\"event\":\"challengeReady\"}" << std::endl;
      if (result == 0 && argc >= 5) {
        until = Clock::now() + std::chrono::seconds(45);
        std::vector<uint8_t> response;
        while (response.empty() && Clock::now() < until) { host.pump(); response = readFile(argv[4]); }
        if (response.empty() || response.size() > 16 * 1024 * 1024) result = 9;
        else {
          host.rejected = false;
          host.module->UpdateSession(3, host.session.data(), uint32_t(host.session.size()), response.data(), uint32_t(response.size()));
          until = Clock::now() + std::chrono::seconds(8);
          while (!host.usableKeys && !host.rejected && Clock::now() < until) host.pump();
          result = host.usableKeys ? 0 : 10;
          std::cout << "{\"event\":\"licenseResult\",\"usable\":" << host.usableKeys << "}" << std::endl;
          if(result==0 && argc>=8) {
            until=Clock::now()+std::chrono::minutes(10);
            bool ready=false;
            while(Clock::now()<until) { std::ifstream job(argv[6],std::ios::binary);if(job){ready=true;break;}host.pump(); }
            result=ready && decryptJob(host.module,host.keyIDs,[&host]{host.pump(false);},argv[6],argv[7],render) ? 0 : 11;
          }
        }
      }
    }
  }
  if (!host.session.empty()) host.module->CloseSession(2, host.session.data(), uint32_t(host.session.size()));
  host.module->Destroy();
  deinitialize();
#ifdef _WIN32
  FreeLibrary(library);
  CoUninitialize();
#else
  dlclose(library);
#endif
  return result;
}
