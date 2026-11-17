// Local capability probe: clear public sample data only, no license exchange.
#define main unused_host_main
#define wmain unused_host_wmain
#include "cdm_probe.cpp"
#undef main
#undef wmain

int main(int argc,char** argv) {
  if(argc!=4&&argc!=5)return 2;
  auto delay=argc==5?std::max(0,std::min(100,std::atoi(argv[4]))):0;
#ifdef _WIN32
  CoInitializeEx(nullptr,COINIT_MULTITHREADED);
  auto library=LoadLibraryExA(argv[1],nullptr,LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|LOAD_LIBRARY_SEARCH_DEFAULT_DIRS);
#else
  auto library=dlopen(argv[1],RTLD_NOW|RTLD_LOCAL);
#endif
  if(!library)return 3;
  auto initialize=reinterpret_cast<void(*)()>(GetProcAddress(library,"InitializeCdmModule_4"));
  auto create=reinterpret_cast<decltype(&CreateCdmInstance)>(GetProcAddress(library,"CreateCdmInstance"));
  if(!initialize||!create)return 4;
  initialize();Host host;
  host.module=static_cast<cdm::ContentDecryptionModule_10*>(create(10,"com.widevine.alpha",18,getHost,&host));
  if(!host.module)return 5;
  host.module->Initialize(false,false,false);for(int i=0;i<10;i++)host.pump();
  std::ifstream input(argv[2],std::ios::binary);auto width=read32(input);auto height=read32(input);auto profile=read32(input);auto extraSize=read32(input);if(!input||extraSize>4096)return 6;
  std::vector<uint8_t> extra(extraSize);input.read(reinterpret_cast<char*>(extra.data()),extraSize);auto mode=std::atoi(argv[3]);if(mode&1)extra=annexBConfiguration(extra);
  cdm::VideoDecoderConfig_2 config{};config.codec=cdm::kCodecH264;config.profile=static_cast<cdm::VideoCodecProfile>(profile);config.format=cdm::kI420;config.coded_size={int32_t(width),int32_t(height)};config.extra_data=extra.data();config.extra_data_size=uint32_t(extra.size());config.encryption_scheme=(mode&4)?cdm::EncryptionScheme::kUnencrypted:cdm::EncryptionScheme::kCenc;
  auto initialized=host.module->InitializeVideoDecoder(config);auto count=read32(input);uint32_t frames=0,consumed=0;cdm::Status status=initialized;
  if(initialized==cdm::kSuccess)for(uint32_t i=0;i<count;i++){
    auto timestamp=int64_t(read64(input));auto size=read32(input);if(size>1024*1024)return 7;std::vector<uint8_t> data(size);input.read(reinterpret_cast<char*>(data.data()),size);if(!input)return 8;
    if((mode&8)&&i==0){auto annex=annexBConfiguration(extra);std::vector<uint8_t> prefix;for(size_t p=0;p+4<annex.size();){size_t end=p+4;while(end+4<=annex.size()&&std::memcmp(annex.data()+end,"\0\0\0\1",4)!=0)++end;if(end+4>annex.size())end=annex.size();auto n=uint32_t(end-p-4);for(int shift=24;shift>=0;shift-=8)prefix.push_back(uint8_t(n>>shift));prefix.insert(prefix.end(),annex.begin()+p+4,annex.begin()+end);p=end;}prefix.insert(prefix.end(),data.begin(),data.end());data=std::move(prefix);size=uint32_t(data.size());}
    if(mode&2)for(size_t p=0;p<data.size();){if(data.size()-p<4)return 9;uint32_t n=(uint32_t(data[p])<<24)|(uint32_t(data[p+1])<<16)|(uint32_t(data[p+2])<<8)|data[p+3];if(n==0||n>data.size()-p-4)return 10;data[p]=data[p+1]=data[p+2]=0;data[p+3]=1;p+=4+n;}
    data.resize(size+64,0);
    cdm::InputBuffer_2 packet{};packet.data=data.data();packet.data_size=size;packet.timestamp=timestamp;packet.encryption_scheme=cdm::EncryptionScheme::kUnencrypted;Frame frame;status=host.module->DecryptAndDecodeFrame(packet,&frame);++consumed;host.pump(false);if(delay)std::this_thread::sleep_for(std::chrono::milliseconds(delay));if(status==cdm::kSuccess)++frames;else if(status!=cdm::kNeedMoreData)break;
  }
  std::cout<<"{\"event\":\"clearCodecTest\",\"mode\":"<<mode<<",\"initialization\":"<<initialized<<",\"lastStatus\":"<<status<<",\"packets\":"<<consumed<<",\"frames\":"<<frames<<"}"<<std::endl;
  if(initialized==cdm::kSuccess)host.module->DeinitializeDecoder(cdm::kStreamTypeVideo);
  host.module->Destroy();auto deinitialize=reinterpret_cast<void(*)()>(GetProcAddress(library,"DeinitializeCdmModule"));if(deinitialize)deinitialize();
#ifdef _WIN32
  FreeLibrary(library);CoUninitialize();
#else
  dlclose(library);
#endif
  return 0;
}
